// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/internal"
	"gorm.io/gorm"
)

const fixtureNode = "vless://11111111-1111-4111-8111-111111111111@192.0.2.10:443?encryption=none&security=tls&type=tcp#Fixture"

type fixtureTransport func(*http.Request) (*http.Response, error)

type fixtureBody struct {
	io.Reader
	closed *bool
}

func (b fixtureBody) Close() error { *b.closed = true; return nil }

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func directFixture(t *testing.T, code int) *atomic.Int32 {
	t.Helper()
	original := http.DefaultTransport
	var calls atomic.Int32
	http.DefaultTransport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"code":"AUTH_FAIL","error":"private-provider-token"}`
		if code == 200 {
			body = base64.StdEncoding.EncodeToString([]byte(fixtureNode))
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	return &calls
}

func TestSubscriptionInvalidDocumentDoesNotRetryThroughOtherExits(t *testing.T) {
	original := http.DefaultTransport
	var calls atomic.Int32
	http.DefaultTransport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<html>expired private-token</html>"))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	proxyHelper(t, "auto", "127.0.0.1:1")
	_, err := fetchLinks("https://fixture.invalid?key=private-token")
	if !isSubscriptionContentError(err) || calls.Load() != 1 || strings.Contains(err.Error(), "private-token") {
		t.Fatal("content error retried or leaked private data")
	}
}

func proxyHelper(t *testing.T, mode, address string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "proxy helper")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nprintf '%s\\n' '"+mode+"' '"+address+"' 'passwall/26.10.1'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAED_SUBSCRIPTION_PROXY_HELPER", file)
	return file
}

// Minimal real SOCKS5 server: the test hostname is resolved by this server,
// never by the client. No external network or real provider credentials.
func socksFixture(t *testing.T, target string) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var requests atomic.Int32
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(3 * time.Second))
				r := bufio.NewReader(connection)
				header := make([]byte, 2)
				if _, err := io.ReadFull(r, header); err != nil || header[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, r, int64(header[1])); err != nil {
					return
				}
				connection.Write([]byte{5, 0})
				request := make([]byte, 4)
				if _, err := io.ReadFull(r, request); err != nil || request[1] != 1 || request[3] != 3 {
					return
				}
				length, err := r.ReadByte()
				if err != nil {
					return
				}
				host := make([]byte, int(length))
				if _, err := io.ReadFull(r, host); err != nil || string(host) != "subscription.invalid" {
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(r, port); err != nil || binary.BigEndian.Uint16(port) != 80 {
					return
				}
				upstream, err := net.DialTimeout("tcp", target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				upstream.SetDeadline(time.Now().Add(3 * time.Second))
				requests.Add(1)
				connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				go io.Copy(upstream, r)
				io.Copy(connection, upstream)
			}()
		}
	}()
	return listener.Addr().String(), &requests
}

func TestSubscriptionAutoFallbackUsesRealSocksAndRemoteDNS(t *testing.T) {
	direct := directFixture(t, 403)
	var cookies atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "passwall/26.10.1" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("PassWall request headers missing")
		}
		if r.URL.Path == "/redirect" {
			http.SetCookie(w, &http.Cookie{Name: "fixture", Value: "accepted"})
			http.Redirect(w, r, "/nodes", http.StatusFound)
			return
		}
		if c, err := r.Cookie("fixture"); err == nil && c.Value == "accepted" {
			cookies.Add(1)
		}
		fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(fixtureNode)))
	}))
	defer server.Close()
	address, socks := socksFixture(t, strings.TrimPrefix(server.URL, "http://"))
	proxyHelper(t, "auto", address)
	t.Setenv("HTTP_PROXY", "http://192.0.2.123:1")
	t.Setenv("NO_PROXY", "*")
	links, err := fetchLinks("http://subscription.invalid/redirect?secret=private-url-token")
	if err != nil || !reflect.DeepEqual(links, []string{fixtureNode}) {
		t.Fatalf("links=%v error=%v", links, err)
	}
	if direct.Load() != 1 || socks.Load() != 2 || cookies.Load() != 1 {
		t.Fatalf("direct=%d socks=%d cookies=%d", direct.Load(), socks.Load(), cookies.Load())
	}
}

func TestSubscriptionAutoPrefersExistingPathAndOffNeverProxies(t *testing.T) {
	direct := directFixture(t, 200)
	proxyHelper(t, "auto", "127.0.0.1:1")
	if _, err := fetchLinks("https://subscription.invalid/nodes"); err != nil {
		t.Fatal(err)
	}
	if direct.Load() != 1 {
		t.Fatal("normal path was not used")
	}
	proxyHelper(t, "off", "127.0.0.1:1")
	if _, err := fetchLinks("https://subscription.invalid/nodes"); err != nil {
		t.Fatal(err)
	}
	if direct.Load() != 2 {
		t.Fatal("off mode did not retain the original path")
	}
}

func TestSubscriptionExplicitProxyNeverFallsBackAndDiscoversChanges(t *testing.T) {
	direct := directFixture(t, 200)
	file := proxyHelper(t, "passwall", "")
	if _, err := fetchLinks("https://subscription.invalid/private-url-token"); err == nil {
		t.Fatal("unavailable explicit proxy succeeded")
	}
	if direct.Load() != 0 {
		t.Fatal("explicit proxy fell back to the system exit")
	}
	if err := os.WriteFile(file, []byte("#!/bin/sh\nprintf '%s\\n' passwall 127.0.0.1:1 passwall/26.10.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	settings, err := readSubscriptionProxy()
	if err != nil || settings.address != "127.0.0.1:1" {
		t.Fatalf("changed cache not reread: %v %v", settings, err)
	}
	if _, err := fetchLinks("https://subscription.invalid/private-url-token"); err == nil || direct.Load() != 0 {
		t.Fatal("failed SOCKS connection fell back to the system exit")
	}
}

func TestSubscriptionProxyValidation(t *testing.T) {
	for _, address := range []string{"192.0.2.1:1080", "user:secret@127.0.0.1:1080", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:1\nsecret", "::1"} {
		t.Run(strings.ReplaceAll(address, "/", "_"), func(t *testing.T) {
			proxyHelper(t, "auto", address)
			if _, err := readSubscriptionProxy(); err == nil {
				t.Fatal("unsafe proxy accepted")
			}
		})
	}
	for _, address := range []string{"localhost:1070", "127.0.0.1:1070", "[::1]:1070"} {
		t.Run(address, func(t *testing.T) {
			proxyHelper(t, "auto", address)
			if _, err := readSubscriptionProxy(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscriptionErrorsHideCredentialsAndCloseBodies(t *testing.T) {
	directFixture(t, 403)
	proxyHelper(t, "off", "")
	_, err := fetchLinks("https://subscription.invalid/private-url-token")
	if err == nil || !strings.Contains(err.Error(), "AUTH_FAIL") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "private-") {
		t.Fatal("subscription credential exposed")
	}
	closed := false
	denied := fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: fixtureBody{strings.NewReader("private-provider-body"), &closed}}, nil
	})
	if _, err := _fetchLinks("https://subscription.invalid/nodes", denied, time.Second); err == nil || !closed {
		t.Fatal("error response body was not closed")
	}
	_, err = _fetchLinks("https://127.0.0.1:1/private-url-token", &http.Transport{}, time.Second)
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("connection error leaked URL: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, fixtureNode) }))
	defer server.Close()
	if _, err := _fetchLinks(server.URL, &http.Transport{}, time.Second); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	oversize := fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (4<<20)+1)))}, nil
	})
	if _, err := _fetchLinks("https://subscription.invalid/nodes", oversize, time.Second); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("unbounded subscription: %v", err)
	}
}

func TestSubscriptionProxyImportManualCronAndFailurePreserveNodes(t *testing.T) {
	directFixture(t, 403)
	var count atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		count.Add(1)
		fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(fixtureNode)))
	}))
	defer server.Close()
	address, _ := socksFixture(t, strings.TrimPrefix(server.URL, "http://"))
	proxyHelper(t, "auto", address)
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	ctx := context.Background()
	d := db.DB(ctx)
	tag := "Proxy fixture"
	var result *ImportResult
	err := d.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = Import(tx, true, &internal.ImportArgument{Link: "http://subscription.invalid/nodes", Tag: &tag})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	id := result.Sub.Subscription.ID
	if _, err := UpdateById(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateCron(ctx, common.EncodeCursor(id), "* * * * *", true); err != nil {
		t.Fatal(err)
	}
	schedulerMu.RLock()
	scheduler := schedulerCache[id]
	schedulerMu.RUnlock()
	if scheduler == nil {
		t.Fatal("cron not registered")
	}
	t.Cleanup(func() { RemoveUpdateScheduler(id) })
	beforeCount := count.Load()
	scheduler.RunAll()
	deadline := time.Now().Add(3 * time.Second)
	for count.Load() == beforeCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count.Load() <= beforeCount {
		t.Fatal("scheduled update did not use SOCKS")
	}
	RemoveUpdateScheduler(id)
	// Wait until the scheduled transaction finishes before taking the snapshot.
	time.Sleep(100 * time.Millisecond)
	var before []db.Node
	if err := d.Where("subscription_id = ?", id).Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if _, err := UpdateById(ctx, id); err == nil {
		t.Fatal("failed proxy download reported success")
	}
	var after []db.Node
	d.Where("subscription_id = ?", id).Find(&after)
	if len(before) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("download failure modified existing nodes")
	}
}
