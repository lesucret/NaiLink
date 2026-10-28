// SPDX-License-Identifier: AGPL-3.0-only
package control

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daeuniverse/dae/common/consts"
	dns "github.com/miekg/dns"
)

func routerFirstDomainFixture(t *testing.T, delay time.Duration, negative bool) (*ControlPlane, *atomic.Int32) {
	t.Helper()
	cp := newTestControlPlaneForRealDomainProbe()
	cp.dialMode = consts.DialMode_Domain
	cp.dnsController = &DnsController{dnsControllerStore: &dnsControllerStore{}}
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		calls.Add(1)
		time.Sleep(delay)
		r := new(dns.Msg)
		r.SetReply(q)
		if negative {
			r.Rcode = dns.RcodeNameError
		} else if q.Question[0].Qtype == dns.TypeA {
			r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 7)}}
		}
		_ = w.WriteMsg(r)
	})}
	ready := make(chan struct{})
	server.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = server.ActivateAndServe() }()
	<-ready
	cp.bootstrapResolvers = []netip.AddrPort{netip.MustParseAddrPort(pc.LocalAddr().String())}
	t.Cleanup(func() { cp.cancel(); _ = server.Shutdown() })
	return cp, &calls
}

func TestRouterFirstDomainUsesVerifiedNameOnFirstConnection(t *testing.T) {
	cp, calls := routerFirstDomainFixture(t, 20*time.Millisecond, false)
	dst := netip.MustParseAddrPort("198.51.100.90:443")
	target, reroute, ip := cp.routerChooseDialTarget(context.Background(), consts.OutboundUserDefinedMin, dst, "first.example")
	if target != "first.example:443" || !reroute || ip {
		t.Fatal("first connection still dialed stale IP", target, reroute, ip)
	}
	if calls.Load() > 2 {
		t.Fatal("duplicate A/AAAA bootstrap probes", calls.Load())
	}
	before := calls.Load()
	target, _, ip = cp.routerChooseDialTarget(context.Background(), consts.OutboundUserDefinedMin, dst, "first.example")
	if target != "first.example:443" || ip || calls.Load() != before {
		t.Fatal("known name was probed again")
	}
}

func TestRouterFirstDomainDoesNotRewriteNegativeDirectOrIPMode(t *testing.T) {
	cp, calls := routerFirstDomainFixture(t, 0, true)
	dst := netip.MustParseAddrPort("198.51.100.90:443")
	target, reroute, ip := cp.routerChooseDialTarget(context.Background(), consts.OutboundUserDefinedMin, dst, "negative.example")
	if target != dst.String() || reroute || !ip {
		t.Fatal("unverified name was trusted")
	}
	before := calls.Load()
	for _, tc := range []struct {
		mode consts.DialMode
		out  consts.OutboundIndex
		name string
	}{{consts.DialMode_Ip, consts.OutboundUserDefinedMin, "other.example"}, {consts.DialMode_Domain, consts.OutboundDirect, "direct.example"}, {consts.DialMode_Domain, consts.OutboundUserDefinedMin, "203.0.113.1"}} {
		cp.dialMode = tc.mode
		target, reroute, ip = cp.routerChooseDialTarget(context.Background(), tc.out, dst, tc.name)
		if target != dst.String() || reroute || !ip {
			t.Fatal("changed explicit IP/direct routing")
		}
	}
	if calls.Load() != before {
		t.Fatal("IP/direct routes started domain probes")
	}
}

func TestRouterFirstDomainConcurrentWaitersShareOneProbe(t *testing.T) {
	cp, calls := routerFirstDomainFixture(t, 20*time.Millisecond, false)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		wg.Go(func() {
			<-start
			target, _, ip := cp.routerChooseDialTarget(context.Background(), consts.OutboundUserDefinedMin, netip.MustParseAddrPort("198.51.100.90:443"), "shared.example")
			if target != "shared.example:443" || ip {
				t.Error("concurrent first connection did not use verified name")
			}
		})
	}
	close(start)
	wg.Wait()
	if calls.Load() > 2 {
		t.Fatal("concurrent waiters duplicated A/AAAA bootstrap probes", calls.Load())
	}
}

func TestRouterFirstDomainWaitIsBoundedAndCancelled(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		cp, _ := routerFirstDomainFixture(t, 350*time.Millisecond, false)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelled {
			time.AfterFunc(15*time.Millisecond, cancel)
		}
		start := time.Now()
		target, reroute, ip := cp.routerChooseDialTarget(ctx, consts.OutboundUserDefinedMin, netip.MustParseAddrPort("198.51.100.90:443"), "slow.example")
		if target != "198.51.100.90:443" || reroute || !ip {
			t.Fatal("wait timeout rewrote an unverified name")
		}
		limit := routerFirstDomainBudget + 100*time.Millisecond
		if cancelled {
			limit = 120 * time.Millisecond
		}
		if time.Since(start) > limit {
			t.Fatal("first domain wait exceeded budget/cancellation", time.Since(start))
		}
	}
	if len(routerFirstDomainWaiters) != 0 {
		t.Fatal("domain wait slot leaked")
	}
}

func TestRouterFirstDomainCapacityDoesNotQueueMoreWork(t *testing.T) {
	cp, calls := routerFirstDomainFixture(t, 0, false)
	for range cap(routerFirstDomainWaiters) {
		routerFirstDomainWaiters <- struct{}{}
	}
	defer func() {
		for range cap(routerFirstDomainWaiters) {
			<-routerFirstDomainWaiters
		}
	}()
	target, reroute, ip := cp.routerChooseDialTarget(context.Background(), consts.OutboundUserDefinedMin, netip.MustParseAddrPort("198.51.100.90:443"), "capacity.example")
	if target != "198.51.100.90:443" || reroute || !ip || calls.Load() != 0 {
		t.Fatal("queued work beyond first-domain capacity")
	}
}
