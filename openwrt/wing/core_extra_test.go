// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"golang.org/x/net/proxy"
)

// An opt-in live fixture can run on the router without importing its database,
// adding user nodes, creating a TUN, or changing the selected running core.
func TestRouterCoreExtraLive(t *testing.T) {
	path, extra := os.Getenv("DAED_TEST_EXTRA_CASES"), os.Getenv("DAED_TEST_EXTRA_ROOT")
	if path == "" || extra == "" {
		t.Skip("private real-device fixtures required")
	}
	var input struct {
		Cases []struct {
			Name, Link, Target, UDP string
			Tor                     bool
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &input) != nil || len(input.Cases) == 0 {
		t.Fatal("invalid private fixture")
	}
	previous := currentRouterCoreManager()
	oldExtra, oldRuntime, oldData := routerExtraRoot, routerCoreRuntimeDir, routerTorDataRoot
	routerExtraRoot, routerCoreRuntimeDir, routerTorDataRoot = extra, filepath.Join(t.TempDir(), "runtime"), filepath.Join(t.TempDir(), "tor")
	conf, _ := coreFixtureConfig(t)
	conf.Dns.Upstream = []daeConfig.KeyableString{"live:udp://127.0.0.1:53"}
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = &routerCoreManager{conf: conf, state: routercore.State{Current: "sing-box"}}
	routerCoreGlobal.Unlock()
	t.Cleanup(func() {
		routerExtraRoot, routerCoreRuntimeDir, routerTorDataRoot = oldExtra, oldRuntime, oldData
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	})
	for _, c := range input.Cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
			defer cancel()
			global := daeConfig.Global{TcpCheckUrl: []string{c.Target + "/generate_204"}, UdpCheckDns: []string{c.UDP}}
			if !c.Tor {
				global.WanInterface = []string{"lo"}
			}
			values, err := ProbeRouterCoreNodes(ctx, []string{c.Link}, global)
			if err != nil || len(values) != 1 || !values[0].Alive {
				t.Fatalf("live probe failed: %v / %+v", err, values)
			}
			t.Logf("real HTTP probe passed: %d ms", values[0].Latency)
			if c.UDP != "" {
				result, err := ProbeRouterCoreUDP(ctx, c.Link, global)
				if err != nil || !result.Alive {
					t.Fatal("real UDP DNS failed", result, err)
				}
			}
			profile, _, _, err := routerProbeConfiguration([]string{c.Link}, global)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			session, err := routerStartProbe(ctx, profile)
			if err != nil {
				t.Fatal(err)
			}
			defer routerStopProbe(session)
			t.Logf("session readiness: %s", time.Since(started).Round(time.Millisecond))
			dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: "probe-0", Password: session.ports.Secret}, &net.Dialer{})
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{Proxy: nil, DialContext: dialer.(proxy.ContextDialer).DialContext}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 35 * time.Second}
			endpoint := c.Target + "/payload"
			if c.Tor {
				endpoint = c.Target + "/generate_204"
			}
			response, err := client.Get(endpoint)
			if err != nil {
				t.Fatal("real request failed")
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
			response.Body.Close()
			if err != nil || (!c.Tor && string(body) != strings.Repeat("protocol-payload", 4096)) || (c.Tor && response.StatusCode != 204) {
				t.Fatal("real response mismatch")
			}
			pid := session.cmd.Process.Pid
			status, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "VmRSS:") {
					t.Log("native core " + line)
				}
			}

			// OpenWrt kernels may omit CONFIG_PROC_CHILDREN. Read PPid from process
			// status instead of depending on /task/*/children being compiled in.
			var childIDs []string
			processFiles, _ := filepath.Glob("/proc/[0-9]*/status")
			for _, file := range processFiles {
				raw, _ := os.ReadFile(file)
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "PPid:") && strings.TrimSpace(strings.TrimPrefix(line, "PPid:")) == strconv.Itoa(pid) {
						childIDs = append(childIDs, filepath.Base(filepath.Dir(file)))
					}
				}
			}

			if c.Tor && len(childIDs) != 1 {
				t.Fatalf("expected one managed Tor child, got %d", len(childIDs))
			}
			if c.Tor {
				for _, child := range childIDs {
					status, _ := os.ReadFile("/proc/" + child + "/status")
					for _, line := range strings.Split(string(status), "\n") {
						if strings.HasPrefix(line, "VmRSS:") {
							t.Log("Tor " + line)
						}
					}
				}
				// Kill only the private test core. The Tor launcher must not leave an
				// orphan holding its cache lock when the native core crashes.
				if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				select {
				case <-session.exit:
				case <-time.After(5 * time.Second):
					t.Fatal("test core did not exit")
				}
				deadline := time.Now().Add(12 * time.Second)
				for _, child := range childIDs {
					for {
						status, err := os.ReadFile("/proc/" + child + "/status")
						if os.IsNotExist(err) || strings.Contains(string(status), "State:\tZ") {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("Tor child survived native core crash")
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
				t.Log("managed Tor terminated after native core crash")
			}
		})
	}
}
