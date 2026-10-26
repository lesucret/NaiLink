// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"golang.org/x/net/proxy"
)

// Use independent protocol servers, not mocked dialers or the embedded dae
// implementation. All listeners and credentials belong to the local fixture.
func TestRouterCoreProtocolInteroperability(t *testing.T) {
	path, binary := os.Getenv("DAED_PROTOCOL_CASES"), os.Getenv("DAED_TEST_SINGBOX")
	if path == "" || binary == "" {
		t.Skip("independent servers and optional native core required")
	}
	var fixture struct {
		Target, UDP string
		Cases       []struct{ Name, Link, Failure string }
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	conf, _ := coreFixtureConfig(t)
	conf.Dns.Upstream = []daeConfig.KeyableString{daeConfig.KeyableString("fixture:udp://" + fixture.UDP)}
	oldBinary, oldDir := routerSingBoxBinary, routerCoreRuntimeDir
	previous := currentRouterCoreManager()
	routerSingBoxBinary, routerCoreRuntimeDir = binary, filepath.Join(t.TempDir(), "private-core")
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = &routerCoreManager{conf: conf, state: routercore.State{Current: "sing-box"}}
	routerCoreGlobal.Unlock()
	t.Cleanup(func() {
		routerSingBoxBinary, routerCoreRuntimeDir = oldBinary, oldDir
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	})
	global := daeConfig.Global{TcpCheckUrl: []string{fixture.Target + "/generate_204"}, UdpCheckDns: []string{fixture.UDP}}
	var links []string
	var cases []struct{ Name, Link, Failure string }
	for _, c := range fixture.Cases {
		if strings.HasPrefix(c.Link, "juicity:") {
			continue
		} // explicitly unsupported by this adapter
		links = append(links, c.Link)
		cases = append(cases, c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	probeConf, names, _, err := routerProbeConfiguration(links, global)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := singBoxConfig(probeConf, nil, sbPorts{Mixed: 41001, API: 41002, DNS: 41003, Secret: "fixture-only"}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkPath := filepath.Join(t.TempDir(), "profile.json")
	data, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(checkPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, binary, "check", "-c", checkPath).CombinedOutput(); err != nil {
		t.Fatalf("native configuration: %s: %v", out, err)
	}
	results, err := ProbeRouterCoreNodes(ctx, links, global)
	if err != nil {
		t.Fatal(err)
	}
	session, err := routerStartProbe(ctx, probeConf)
	if err != nil {
		t.Fatal(err)
	}
	defer routerStopProbe(session)
	udpSeen := map[string]bool{}
	for index, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Failure != "" {
				if results[index].Alive {
					t.Fatal("accepted invalid server certificate")
				}
				return
			}
			if !results[index].Alive {
				t.Fatalf("native HEAD probe failed: %+v", results[index])
			}
			dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: names[index], Password: session.ports.Secret}, &net.Dialer{Timeout: 6 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 6 * time.Second}
			for _, endpoint := range []string{"/payload", "/stream"} {
				response, err := client.Get(fixture.Target + endpoint)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || string(body) != strings.Repeat("protocol-payload", 4096) {
					t.Fatal("payload/stream mismatch", err)
				}
			}
			family := strings.SplitN(c.Link, ":", 2)[0]
			if family == "hy2" {
				family = "hysteria2"
			}
			if !udpSeen[family] {
				udpSeen[family] = true
				result, err := ProbeRouterCoreUDP(ctx, c.Link, global)
				if err != nil || !result.Alive {
					t.Fatal("native UDP DNS failed", result, err)
				}
			}
		})
	}
}
