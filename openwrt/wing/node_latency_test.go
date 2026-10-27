// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/routercore"
	dialer "github.com/daeuniverse/dae/component/outbound/dialer"
	"github.com/graph-gophers/graphql-go"
	"github.com/sirupsen/logrus"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A proxy can accept TCP and then never send its encrypted response salt.
// HTTP cancellation must interrupt that Read before Close waits on its lock.
func TestRouterLatencySilentShadowsocksReleasesBatchSlots(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var peers []net.Conn
	var servers sync.WaitGroup
	accepted := make(chan struct{})
	servers.Add(1)
	go func() {
		defer servers.Done()
		defer close(accepted)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			peers = append(peers, c)
			mu.Unlock()
			servers.Add(1)
			go func() {
				defer servers.Done()
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	cleanup := func() {
		_ = listener.Close()
		<-accepted
		mu.Lock()
		for _, c := range peers {
			_ = c.Close()
		}
		mu.Unlock()
		servers.Wait()
	}
	defer cleanup()
	log := logrus.New()
	log.SetOutput(io.Discard)
	option := &dialer.GlobalOption{Log: log, CheckInterval: time.Minute, TcpCheckOptionRaw: dialer.TcpCheckOptionRaw{Log: log, Raw: []string{"http://127.0.0.1/generate_204", "127.0.0.1"}, Method: http.MethodGet, ResolverNetwork: "udp"}}
	user := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:fixture-silent-server"))
	nodes := make([]db.Node, latencyProbeConcurrency)
	for i := range nodes {
		nodes[i].ID = uint(i + 1)
		nodes[i].Link = "ss://" + user + "@" + listener.Addr().String()
	}
	for batch := 0; batch < 2; batch++ {
		done := make(chan error, 1)
		go func() {
			results, err := runLatencyProbes(context.Background(), nodes, func(ctx context.Context, n *db.Node) *LatencyResolver {
				ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
				return testSingleNodeLatency(ctx, option, n)
			})
			if err == nil {
				for _, r := range results {
					if r == nil || r.AliveVal || r.MessageV == nil || *r.MessageV != "timeout" {
						err = fmt.Errorf("silent SS did not return a timeout")
						break
					}
				}
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			cleanup()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			t.Fatal("silent Shadowsocks held batch slots after the test deadline")
		}
		if len(latencyProbeSlots) != 0 {
			t.Fatal("test slots were not released")
		}
	}
}

func TestRouterLatencyFailureCategoriesDoNotLeakCredentials(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{x509.UnknownAuthorityError{}, "tls_failed"},
		{&net.DNSError{Err: "private host", Name: "fixture.invalid"}, "dns_failed"},
		{syscall.ECONNREFUSED, "connection_refused"},
		{io.EOF, "connection_closed"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "cancelled"},
		{fmt.Errorf("authentication failed for private-password"), "authentication_failed"},
		{fmt.Errorf("unexpected status code: 403 private-provider-body"), "check_target_failed"},
	} {
		wrapped := &url.Error{Op: "Get", URL: "anytls://private-password@fixture.invalid", Err: c.err}
		if got := latencyFailureCode(wrapped); got != c.code {
			t.Fatalf("expected %s, got %s", c.code, got)
		}
	}
}

func TestRouterLatencyThroughRealSocksAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(routercore.ManualProbeTimeout + 2*time.Second))
				greeting := make([]byte, 2)
				if _, err := io.ReadFull(c, greeting); err != nil {
					return
				}
				_, _ = io.CopyN(io.Discard, c, int64(greeting[1]))
				_, _ = c.Write([]byte{5, 0})
				header := make([]byte, 4)
				if _, err := io.ReadFull(c, header); err != nil {
					return
				}
				length := 0
				switch header[3] {
				case 1:
					length = 4
				case 4:
					length = 16
				case 3:
					b := make([]byte, 1)
					_, _ = io.ReadFull(c, b)
					length = int(b[0])
				}
				_, _ = io.CopyN(io.Discard, c, int64(length+2))
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				// An unusable IPv6 path must not delay a working IPv4 result.
				if header[3] == 4 {
					_, _ = io.Copy(io.Discard, c)
					return
				}
				request, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				if request.URL.Path == "/slow" {
					time.Sleep(500 * time.Millisecond)
				}
				if request.URL.Path == "/delayed" {
					time.Sleep(3200 * time.Millisecond)
				}
				if request.URL.Path == "/stall" {
					_, _ = io.Copy(io.Discard, c)
					return
				}
				time.Sleep(20 * time.Millisecond)
				_, _ = io.WriteString(c, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
			}()
		}
	}()
	log := logrus.New()
	log.SetOutput(io.Discard)
	option := &dialer.GlobalOption{Log: log, CheckInterval: time.Minute, TcpCheckOptionRaw: dialer.TcpCheckOptionRaw{Log: log, Raw: []string{"http://127.0.0.1/generate_204", "127.0.0.1"}, Method: http.MethodGet, ResolverNetwork: "udp"}}
	n := db.Node{Link: "socks5://" + listener.Addr().String() + "#Fixture"}
	n.ID = 7
	result := testSingleNodeLatency(context.Background(), option, &n)
	if !result.AliveVal || result.LatencyMsV == nil || *result.LatencyMsV < 10 || result.NodeID != 7 {
		t.Fatalf("real HTTP-over-proxy latency failed: %#v", result)
	}
	option.TcpCheckOptionRaw.Reset()
	option.TcpCheckOptionRaw.Raw = []string{"http://localhost/generate_204", "127.0.0.1", "::1"}
	startDual := time.Now()
	result = testSingleNodeLatency(context.Background(), option, &n)
	if !result.AliveVal || time.Since(startDual) > 400*time.Millisecond {
		t.Fatalf("working IPv4 waited for stalled IPv6: %v %#v", time.Since(startDual), result)
	}
	t.Logf("IPv4 HTTP response returned in %v despite stalled IPv6", time.Since(startDual))
	option.TcpCheckOptionRaw.Reset()
	option.TcpCheckOptionRaw.Raw = []string{"http://127.0.0.1/delayed", "127.0.0.1"}
	result = testSingleNodeLatency(context.Background(), option, &n)
	if !result.AliveVal || result.LatencyMsV == nil || *result.LatencyMsV < 3200 {
		t.Fatal("usable slow response was rejected or its actual duration was hidden", result)
	}
	option.TcpCheckOptionRaw.Reset()
	option.TcpCheckOptionRaw.Raw = []string{"http://127.0.0.1/generate_204", "127.0.0.1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	result = testSingleNodeLatency(ctx, option, &n)
	if result.AliveVal || time.Since(start) > time.Second {
		t.Fatal("cancelled probe did not stop promptly")
	}
	option.TcpCheckOptionRaw.Reset()
	option.TcpCheckOptionRaw.Raw = []string{"http://127.0.0.1/slow", "127.0.0.1"}
	ctx, cancel = context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start = time.Now()
	result = testSingleNodeLatency(ctx, option, &n)
	if result.AliveVal || time.Since(start) > 300*time.Millisecond {
		t.Fatal("active slow proxy probe did not respect cancellation")
	}
	option.TcpCheckOptionRaw.Reset()
	option.TcpCheckOptionRaw.Raw = []string{"http://127.0.0.1/stall", "127.0.0.1"}
	start = time.Now()
	result = testSingleNodeLatency(context.Background(), option, &n)
	if result.AliveVal || time.Since(start) > routercore.ManualProbeTimeout+500*time.Millisecond {
		t.Fatal("stalled HTTP probe exceeded interactive deadline")
	}
	t.Logf("Stalled HTTP request stopped after %v", time.Since(start))
}

func TestRouterLatencyGlobalConcurrencyAcrossRequests(t *testing.T) {
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for request := 0; request < 8; request++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runLatencyProbes(context.Background(), make([]db.Node, 8), func(_ context.Context, _ *db.Node) *LatencyResolver {
				n := active.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(5 * time.Millisecond)
				active.Add(-1)
				return &LatencyResolver{}
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 8 || active.Load() != 0 {
		t.Fatalf("global limit violated: peak %d active %d", peak.Load(), active.Load())
	}
}

func TestRouterLatencyBatchLimit(t *testing.T) {
	ids := make([]graphql.ID, 9)
	if _, err := TestLatencies(context.Background(), &ids); err == nil || !strings.Contains(err.Error(), "8 nodes") {
		t.Fatalf("expected pre-DB limit error, got %v", err)
	}
}

func TestRouterLatencyQueryDoesNotProbe(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// No selected configuration exists: the old read path would fail before
	// returning anything, as it tried to create new probe dialers on every poll.
	if err := db.DB(context.Background()).Create(&db.Node{Link: "invalid-private-node", Name: "fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := QueryLatencies(context.Background(), nil)
	if err != nil || len(result) != 0 {
		t.Fatalf("cache read should not dial or need a selected config: %v %v", result, err)
	}
}

func TestRouterLatencyCancellationAndConcurrency(t *testing.T) {
	nodes := make([]db.Node, 8)
	for i := range nodes {
		nodes[i].ID = uint(i + 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var started, active, peak atomic.Int32
	start := time.Now()
	result, err := runLatencyProbes(ctx, nodes, func(ctx context.Context, node *db.Node) *LatencyResolver {
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		started.Add(1)
		<-ctx.Done()
		active.Add(-1)
		return &LatencyResolver{NodeID: node.ID}
	})
	if err != context.DeadlineExceeded || result != nil || started.Load() != 8 || active.Load() != 0 || peak.Load() > 8 || time.Since(start) > time.Second {
		t.Fatalf("unbounded/cancelled probes: err=%v started=%d active=%d peak=%d", err, started.Load(), active.Load(), peak.Load())
	}
}

func TestRouterLatencyResultsKeepIDsAndOrder(t *testing.T) {
	nodes := []db.Node{{}, {}}
	nodes[0].ID = 7
	nodes[1].ID = 3
	results, err := runLatencyProbes(context.Background(), nodes, func(_ context.Context, n *db.Node) *LatencyResolver {
		return &LatencyResolver{NodeID: n.ID, AliveVal: true}
	})
	if err != nil || results[0].NodeID != 7 || results[1].NodeID != 3 {
		t.Fatalf("lost result IDs/order: %v %v", results, err)
	}
}
