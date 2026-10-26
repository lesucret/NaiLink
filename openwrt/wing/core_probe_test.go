// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"github.com/sirupsen/logrus"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	daeConfig "github.com/daeuniverse/dae/config"
)

type routerProbeDialFunc func(context.Context, string, string) (net.Conn, error)

func (f routerProbeDialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func TestRouterCoreHTTPRacesFixedTargetsAndHonorsMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.Host != "check.invalid" {
					t.Error("test changed HTTP method or Host", r.Method, r.Host)
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			cancelled := make(chan struct{}, 1)
			started := make(chan struct{})
			dial := routerProbeDialFunc(func(ctx context.Context, _, address string) (net.Conn, error) {
				switch address {
				case "192.0.2.1:80":
					close(started)
					<-ctx.Done()
					cancelled <- struct{}{}
					return nil, ctx.Err()
				case "[2001:db8::1]:80":
					select {
					case <-started:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				default:
					t.Error("fixed test IP was not used", address)
					return nil, errors.New("unexpected target")
				}
			})
			ctx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			result := routerProbeHTTP(ctx, dial, daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid/generate_204", "192.0.2.1", "2001:db8::1"}, TcpCheckHttpMethod: method})
			if !result.Alive || result.Message != "" {
				t.Fatal("working second family lost", result)
			}
			// net/http may finish the cancelled request before its dial goroutine
			// is scheduled again. Wait for cleanup rather than inspecting a racy
			// instantaneous channel length.
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Fatal("failed family's dial was not cancelled")
			}
		})
	}
}

func TestRouterCoreHTTPPreservesRealConnectionCostAndStatus(t *testing.T) {
	var mu sync.Mutex
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer server.Close()
	dial := routerProbeDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		connections++
		mu.Unlock()
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})
	global := daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid/generate_204", "192.0.2.1"}}
	for range 2 {
		r := routerProbeHTTP(context.Background(), dial, global)
		if !r.Alive || r.Latency < 60 {
			t.Fatal("connection/handshake time was omitted", r)
		}
	}
	mu.Lock()
	if connections != 2 {
		t.Error("manual tests reused a connection", connections)
	}
	mu.Unlock()
	global.TcpCheckUrl[0] = "http://check.invalid/generate_200"
	if r := routerProbeHTTP(context.Background(), dial, global); r.Alive || r.Message != "check_target_failed" {
		t.Fatal("unexpected status accepted as healthy", r)
	}
}

func TestRouterCoreHTTPParentCancellation(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stop()
	dial := routerProbeDialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	result := routerProbeHTTP(ctx, dial, daeConfig.Global{TcpCheckUrl: []string{"http://check.invalid", "192.0.2.1", "2001:db8::1"}})
	if result.Alive || result.Message != "cancelled" {
		t.Fatal(result)
	}
}

func TestRouterCoreProbeFailureDoesNotHideTimeoutOrLeakValues(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{&url.Error{Op: "HEAD", URL: "https://secret.invalid", Err: context.DeadlineExceeded}, "timeout"},
		{context.Canceled, "cancelled"},
		{io.ErrUnexpectedEOF, "connection_closed"},
		{errors.New("private-password server-controlled failure"), "unreachable"},
	} {
		if got := routerCoreProbeFailure(c.err); got != c.want {
			t.Fatal(got, c.want)
		}
	}
}

func TestRouterCoreProbeNativeErrorsAreScopedAndRedacted(t *testing.T) {
	p := &routerProbeErrors{codes: map[string]string{}}
	cases := []struct{ message, code string }{
		{"dial tcp private.example:443: i/o timeout", "timeout"},
		{"tls: failed to verify certificate: private-value", "tls_failed"},
		{"REALITY verification failed private-value", "authentication_failed"},
		{"lookup private.example: no such host", "dns_failed"},
		{"dial tcp private.example: connection refused", "connection_refused"},
		{"read private.example: connection reset by peer", "connection_closed"},
	}
	var wg sync.WaitGroup
	for i, c := range cases {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "probe-" + string(rune('0'+i))
			_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:" + name + "]: " + c.message})
		}()
	}
	wg.Wait()
	for i, c := range cases {
		if got := p.message("probe-"+string(rune('0'+i)), "unreachable"); got != c.code || strings.Contains(got, "private") {
			t.Fatal(got, c.code)
		}
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:real-user-label]: certificate failed"})
	if got := p.message("real-user-label", "unreachable"); got != "unreachable" {
		t.Fatal("accepted a non-probe log")
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: authentication failed"})
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: i/o timeout"})
	if got := p.message("probe-0", "unreachable"); got != "authentication_failed" {
		t.Fatal("lost actionable native error", got)
	}
}
