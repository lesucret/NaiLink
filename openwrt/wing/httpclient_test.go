// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/daeuniverse/dae-wing/routercore"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	dialer "github.com/daeuniverse/dae/component/outbound/dialer"
	D "github.com/daeuniverse/outbound/dialer"
	"github.com/daeuniverse/outbound/netproxy"
	"github.com/sirupsen/logrus"
)

func TestRouterHTTPDialFallsBackFromStalledIPv6(t *testing.T) {
	addresses := []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("192.0.2.1")}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	var tried []netip.Addr
	conn, err := dialRouterAddresses(ctx, addresses, func(attempt context.Context, ip netip.Addr) (net.Conn, error) {
		tried = append(tried, ip)
		if ip.Is6() {
			<-attempt.Done()
			return nil, attempt.Err()
		}
		return left, nil
	})
	if err != nil || conn != left || len(tried) != 2 || ctx.Err() != nil {
		t.Fatalf("failed to try working IPv4 address: %v, tried %v", err, tried)
	}
}

func TestRouterHTTPDialStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	_, err := dialRouterAddresses(ctx, []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}, func(attempt context.Context, _ netip.Addr) (net.Conn, error) {
		calls++
		cancel()
		<-attempt.Done()
		return nil, attempt.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled download continued dialing: %v, calls=%d", err, calls)
	}
}

func TestRouterHTTPDialPreservesHealthyConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	conn, err := dialRouterAddresses(ctx, []netip.Addr{netip.MustParseAddr("192.0.2.1")}, func(context.Context, netip.Addr) (net.Conn, error) { return left, nil })
	if err != nil {
		t.Fatal(err)
	}
	go right.Write([]byte("ok"))
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 2)
	if n, err := conn.Read(buf); err != nil || n != 2 || string(buf) != "ok" {
		t.Fatalf("dial attempt cancellation damaged established connection: %d, %v", n, err)
	}
}

func TestRouterHTTPProtocolDownload(t *testing.T) {
	path := os.Getenv("DAED_PROTOCOL_CASES")
	if path == "" {
		t.Skip("independent loopback server required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Target string
		Cases  []struct {
			Name, Link, Failure string
			Clash               routercore.Object
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, item := range fixture.Cases {
		if item.Clash != nil {
			var e error
			item.Link, e = routercore.ClashLink(item.Clash)
			if e != nil {
				t.Fatal(item.Name, e)
			}
		}
		t.Run(item.Name, func(t *testing.T) {
			log := logrus.New()
			log.SetOutput(io.Discard)
			options := &dialer.GlobalOption{Log: log, ExtraOption: D.ExtraOption{TlsImplementation: "tls", UtlsImitate: "chrome_auto"}}
			options.UDPHopInterval = 5 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			d, err := dialer.NewFromLinkContext(ctx, options, dialer.InstanceOption{DisableCheck: true}, item.Link, "")
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			transport := &http.Transport{DisableKeepAlives: true, DialContext: func(parent context.Context, network, address string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				ip, err := netip.ParseAddr(host)
				if err != nil {
					return nil, err
				}
				return dialRouterAddresses(parent, []netip.Addr{ip}, func(attempt context.Context, _ netip.Addr) (net.Conn, error) {
					conn, err := d.DialContext(attempt, network, address)
					if err != nil {
						return nil, err
					}
					return &routerSubscriptionConn{Conn: &netproxy.FakeNetConn{Conn: conn}}, nil
				})
			}}
			defer transport.CloseIdleConnections()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fixture.Target+"/payload", nil)
			response, err := (&http.Client{Transport: transport}).Do(request)
			if item.Failure != "" {
				if err == nil {
					response.Body.Close()
					t.Fatal("untrusted TLS certificate accepted")
				}
				return
			}
			if err != nil {
				t.Fatal("download after dial context release failed:", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != strings.Repeat("protocol-payload", 4096) {
				t.Fatal("download corrupted or interrupted", err)
			}
		})
	}
}
