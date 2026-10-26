// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	dialer "github.com/daeuniverse/dae/component/outbound/dialer"
	"github.com/graph-gophers/graphql-go"
	"github.com/sirupsen/logrus"
)

type UDPTestResolver struct {
	*LatencyResolver
	TargetV string
}

func (r *UDPTestResolver) Target() string { return r.TargetV }

// Explicit, one-node diagnostic. It never updates HTTP latency or running health
// history, and shares the existing capacity limits with HTTP probes.
func TestNodeUDP(parent context.Context, id graphql.ID) (*UDPTestResolver, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	select {
	case latencyRequestSlot <- struct{}{}:
		defer func() { <-latencyRequestSlot }()
	default:
		return nil, fmt.Errorf("node tests busy; retry shortly")
	}
	ids := []graphql.ID{id}
	nodes, err := latencyProbeNodes(ctx, &ids)
	if err != nil || len(nodes) != 1 {
		return nil, fmt.Errorf("node not found")
	}
	select {
	case latencyProbeSlots <- struct{}{}:
		defer func() { <-latencyProbeSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var model db.Config
	if err := db.DB(ctx).Where("selected = ?", true).First(&model).Error; err != nil {
		return nil, fmt.Errorf("selected configuration unavailable")
	}
	parsed, err := dae.ParseConfig(&model.Global, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid global configuration")
	}
	if dae.RouterCoreName() == "sing-box" {
		value, probeErr := dae.ProbeRouterCoreUDP(ctx, nodes[0].Link, parsed.Global)
		if probeErr != nil {
			return nil, fmt.Errorf("sing-box UDP test unavailable")
		}
		r := &UDPTestResolver{LatencyResolver: &LatencyResolver{NodeID: nodes[0].ID, AliveVal: value.Alive, TestedAtV: value.Tested}}
		if len(parsed.Global.UdpCheckDns) > 0 {
			r.TargetV = parsed.Global.UdpCheckDns[0]
		}
		if value.Alive {
			r.LatencyMsV = &value.Latency
		} else {
			r.MessageV = &value.Message
		}
		return r, nil
	}
	log := logrus.New()
	log.SetOutput(io.Discard)
	option := dialer.NewGlobalOption(&parsed.Global, log)
	// This namespace belongs only to this explicit diagnostic. Retire cached
	// gRPC/meek transports after the temporary dialer, including early failures.
	defer dialer.CleanupTransportCacheNamespace(option.TransportCacheNamespace)
	r := &UDPTestResolver{LatencyResolver: &LatencyResolver{NodeID: nodes[0].ID, TestedAtV: time.Now()}}
	raw := &option.CheckDnsOptionRaw
	if len(raw.Raw) > 0 {
		r.TargetV = raw.Raw[0]
	}
	target, err := dialer.ParseCheckDnsOption(ctx, raw.Raw, raw.ResolverNetwork)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || target == nil || target.Ip46 == nil || (!target.Ip4.IsValid() && !target.Ip6.IsValid()) {
		message := "check_target_failed"
		r.MessageV = &message
		return r, nil
	}
	d, err := dialer.NewFromLinkContext(ctx, option, dialer.InstanceOption{DisableCheck: true}, nodes[0].Link, "")
	if err != nil {
		message := "invalid_node"
		r.MessageV = &message
		return r, nil
	}
	defer d.Close()
	start := time.Now()
	err = probeUDP(ctx, []netip.Addr{target.Ip4, target.Ip6}, func(ctx context.Context, ip netip.Addr) error {
		ok, err := d.DnsCheck(ctx, netip.AddrPortFrom(ip, target.DnsPort), raw.ResolverNetwork)
		if err == nil && !ok {
			return fmt.Errorf("empty UDP DNS answer")
		}
		return err
	})
	r.TestedAtV = time.Now()
	if err == nil {
		r.AliveVal = true
		ms := int32(time.Since(start).Milliseconds())
		r.LatencyMsV = &ms
	} else {
		message := udpFailureCode(err)
		r.MessageV = &message
	}
	return r, nil
}

func udpFailureCode(err error) string {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "unsupported") || strings.Contains(text, "not support") || strings.Contains(text, "not implemented") {
		return "udp_unsupported"
	}
	return latencyFailureCode(err)
}

// First successful family wins; cancellation drains every attempt before the
// temporary protocol dialer is closed, including an unavailable second family.
func probeUDP(parent context.Context, addresses []netip.Addr, probe func(context.Context, netip.Addr) error) error {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	results := make(chan error, len(addresses))
	var workers sync.WaitGroup
	for _, ip := range addresses {
		if !ip.IsValid() {
			continue
		}
		workers.Add(1)
		go func(ip netip.Addr) {
			defer workers.Done()
			results <- probe(ctx, ip)
		}(ip)
	}
	go func() { workers.Wait(); close(results) }()
	var failure error
	alive := false
	for err := range results {
		if err == nil {
			alive = true
			cancel()
		}
		if err != nil && (failure == nil || udpFailureCode(failure) == "timeout") {
			failure = err
		}
	}
	if alive {
		return nil
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if failure == nil {
		return fmt.Errorf("no UDP target address")
	}
	return failure
}
