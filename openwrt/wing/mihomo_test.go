// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

func TestPrivateListenerSurvivesAutomaticPortExhaustion(t *testing.T) {
	calls := 0
	l, err := routerListenLoopback(func(network, address string) (net.Listener, error) {
		calls++
		if address == "127.0.0.1:0" || calls < 3 {
			return nil, syscall.EADDRINUSE
		}
		if !strings.HasPrefix(address, "127.0.0.1:") { t.Fatal("listener escaped loopback") }
		return net.Listen(network, address)
	})
	if err != nil { t.Fatal(err) }
	l.Close()
	if calls < 3 { t.Fatal("occupied listener was not retried") }
	calls = 0
	_, err = routerListenLoopback(func(_, _ string) (net.Listener, error) { calls++; return nil, syscall.EADDRINUSE })
	if err == nil || calls != 65 { t.Fatal("port exhaustion was not bounded", calls, err) }
	calls = 0
	_, err = routerListenLoopback(func(_, _ string) (net.Listener, error) { calls++; return nil, syscall.EACCES })
	if err != syscall.EACCES || calls != 1 { t.Fatal("retried a permission error", calls, err) }
}

func TestPrivatePortPairRetriesOccupiedUDP(t *testing.T) {
	busy, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	defer busy.Close()
	calls := 0
	tcp, udp, err := routerLoopbackPair(func(network, address string) (net.Listener, error) {
		calls++
		if calls == 1 { return net.Listen(network, busy.LocalAddr().String()) }
		return net.Listen(network, address)
	}, net.ListenPacket)
	if err != nil { t.Fatal(err) }
	defer tcp.Close()
	defer udp.Close()
	if calls < 2 || tcp.Addr().String() != udp.LocalAddr().String() { t.Fatal("TCP/UDP pair was not reserved together") }
	if _, err := busy.WriteTo([]byte("still owned"), busy.LocalAddr()); err != nil { t.Fatal("changed another listener", err) }
}

func TestPrivateListenerRealPortExhaustion(t *testing.T) {
	if os.Getenv("DAED_ISOLATED_PORT_EXHAUSTION") != "1" { t.Skip("requires a disposable network namespace") }
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	var low, high int
	if err != nil { t.Fatal(err) }
	fmt.Sscanf(string(raw), "%d %d", &low, &high)
	if low < 32768 || high < low || high-low > 127 { t.Fatal("refusing to exhaust a normal system port range") }
	var held []net.Listener
	defer func() { for _, l := range held { l.Close() } }()
	for len(held) <= high-low+1 {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			if !errors.Is(e, syscall.EADDRINUSE) { t.Fatal(e) }
			break
		}
		held = append(held, l)
	}
	if len(held) > high-low+1 { t.Fatal("automatic ports were not exhausted") }
	l, err := routerListenLoopback(net.Listen)
	if err != nil { t.Fatal("private listener could not recover", err) }
	defer l.Close()
	if l.Addr().(*net.TCPAddr).Port >= 30000 { t.Fatal("fallback still used automatic range") }
}

func TestNativeReadinessRequiresAuthenticatedListeners(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer controller.Close()
	listen := func() net.Listener {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil { t.Fatal(err) }
		t.Cleanup(func() { l.Close() })
		return l
	}
	apiAddr := controller.Listener.Addr().(*net.TCPAddr)
	mixed, dns := listen(), listen()
	s := &routerSingBoxSession{ports: sbPorts{API: apiAddr.Port, Mixed: mixed.Addr().(*net.TCPAddr).Port, DNS: dns.Addr().(*net.TCPAddr).Port, Secret: "test-only"}, exit: make(chan struct{})}
	// An available controller alone must not release a user test before SOCKS
	// authentication is working. Model a slow core that has only bound its port.
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	if err := s.Ready(ctx); err == nil { t.Fatal("controller-only startup reported ready") }
	cancel()
	go func() {
		for {
			c, err := mixed.Accept()
			if err != nil { return }
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				var greeting [3]byte
				if _, err := io.ReadFull(c, greeting[:]); err != nil { return }
				c.Write([]byte{5, 2})
				var auth [2]byte
				if _, err := io.ReadFull(c, auth[:]); err != nil { return }
				user := make([]byte, int(auth[1])+1)
				if _, err := io.ReadFull(c, user); err != nil { return }
				password := make([]byte, int(user[len(user)-1]))
				if _, err := io.ReadFull(c, password); err != nil { return }
				if string(user[:len(user)-1]) != "nailink" || string(password) != s.ports.Secret { c.Write([]byte{1, 1}); return }
				c.Write([]byte{1, 0})
			}()
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Ready(ctx); err != nil { t.Fatal(err) }
}

func TestRouterMihomoCanonicalRulesAndNativeCheck(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	before := restartConfigKey(conf)
	ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "test-only"}
	canonical, _, err := coreCanonicalConfig(conf, dirs, ports, true, "mihomo")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := mihomoFromCanonical(canonical, conf, ports)
	if err != nil {
		t.Fatal(err)
	}
	if before != restartConfigKey(conf) {
		t.Fatal("changed canonical user configuration")
	}
	if profile["routing-mark"] != 0x4e4102 || profile["mode"] != "rule" || profile["tun"].(sbObject)["device"] != routercore.TunName || len(profile["rule-providers"].(map[string]any)) == 0 {
		t.Fatal("missing routing conversion")
	}
	delete(profile, "_nailink_dns")
	if binary := os.Getenv("DAED_TEST_MIHOMO"); binary != "" {
		dir := t.TempDir()
		raw, _ := json.Marshal(profile)
		path := filepath.Join(dir, "config.json")
		os.WriteFile(path, raw, 0600)
		output, err := exec.Command(binary, "-t", "-d", dir, "-f", path).CombinedOutput()
		if err != nil {
			t.Fatal(string(output), err)
		}
	}
}

func TestRouterMihomoProtocolInteroperability(t *testing.T) {
	path, binary := os.Getenv("DAED_PROTOCOL_CASES"), os.Getenv("DAED_TEST_MIHOMO")
	if path == "" || binary == "" {
		t.Skip("independent protocol servers and fixed Mihomo required")
	}
	var fixture struct {
		Target, UDP        string
		Cases, NativeCases []routerInteropCase
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid fixture", err)
	}
	conf, _ := coreFixtureConfig(t)
	conf.Dns.Upstream = []daeConfig.KeyableString{daeConfig.KeyableString("test:udp://" + fixture.UDP)}
	conf.Dns.Routing.Request.Rules = nil
	conf.Dns.Routing.Request.Fallback = "test"
	oldBinary, oldDir, previous := routerMihomoBinary, routerCoreRuntimeDir, currentRouterCoreManager()
	routerMihomoBinary, routerCoreRuntimeDir = binary, filepath.Join(t.TempDir(), "private-core")
	routerCoreGlobal.Lock()
	routerCoreGlobal.manager = &routerCoreManager{conf: conf, state: routercore.State{Current: "mihomo"}}
	routerCoreGlobal.Unlock()
	defer func() {
		routerMihomoBinary, routerCoreRuntimeDir = oldBinary, oldDir
		routerCoreGlobal.Lock()
		routerCoreGlobal.manager = previous
		routerCoreGlobal.Unlock()
	}()
	global := daeConfig.Global{WanInterface: []string{"lo"}, TcpCheckUrl: []string{fixture.Target + "/generate_204"}, UdpCheckDns: []string{fixture.UDP}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var cases []routerInteropCase
	var links []string
	for _, c := range append(fixture.Cases, fixture.NativeCases...) {
		n, e := routerParseCoreNode("mihomo", c.Link, "classify", routercore.NodeOptions{})
		if e == nil {
			for _, out := range routercore.ExpandNode(n) {
				if _, e = routercore.MihomoNode(out); e != nil {
					break
				}
			}
		}
		if e != nil {
			t.Logf("adapter explicitly rejects %s: %s", c.Name, e.Error())
			continue
		}
		cases = append(cases, c)
		links = append(links, c.Link)
	}
	if len(links) < 15 {
		t.Fatal("too few actual protocol cases", len(links))
	}
	checkConf, _, _, err := routerProbeConfiguration(links, global)
	if err != nil {
		t.Fatal(err)
	}
	checkPorts := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "fixture-only"}
	checkProfile, _, err := coreCanonicalConfig(checkConf, nil, checkPorts, false, "mihomo")
	if err != nil {
		t.Fatal(err)
	}
	checkProfile, err = mihomoFromCanonical(checkProfile, checkConf, checkPorts)
	if err != nil {
		t.Fatal(err)
	}
	delete(checkProfile, "_nailink_dns")
	checkDir := t.TempDir()
	checkPath := filepath.Join(checkDir, "config.json")
	checkRaw, _ := json.Marshal(checkProfile)
	os.WriteFile(checkPath, checkRaw, 0600)
	if output, e := exec.Command(binary, "-t", "-d", checkDir, "-f", checkPath).CombinedOutput(); e != nil {
		t.Fatalf("private fixture configuration: %s: %v", output, e)
	}
	results, err := ProbeRouterCoreNodes(ctx, links, global)
	if err != nil {
		t.Fatal(err)
	}
	udpDone := map[string]bool{}
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			g := global
			if c.TargetAlt != "" {
				g.TcpCheckUrl = []string{c.TargetAlt + "/generate_204"}
				r, e := ProbeRouterCoreNodes(ctx, []string{c.Link}, g)
				if e != nil {
					t.Fatal(e)
				}
				results[i] = r[0]
			}
			if c.Failure != "" {
				if results[i].Alive {
					t.Fatal("accepted invalid certificate")
				}
				return
			}
			if !results[i].Alive {
				t.Fatalf("actual HTTP probe failed: %+v", results[i])
			}
			probe, _, _, e := routerProbeConfiguration([]string{c.Link}, g)
			if e != nil {
				t.Fatal(e)
			}
			session, e := routerStartProbe(ctx, probe)
			if e != nil {
				t.Fatal(e)
			}
			defer routerStopProbe(session)
			dial, e := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: "probe-0", Password: session.ports.Secret}, &net.Dialer{})
			if e != nil {
				t.Fatal(e)
			}
			transport := &http.Transport{Proxy: nil, DialContext: dial.(proxy.ContextDialer).DialContext}
			defer transport.CloseIdleConnections()
			target := fixture.Target
			if c.TargetAlt != "" {
				target = c.TargetAlt
			}
			response, e := (&http.Client{Transport: transport, Timeout: 8 * time.Second}).Get(target + "/stream")
			if e != nil {
				t.Fatal(e)
			}
			payload, e := io.ReadAll(response.Body)
			response.Body.Close()
			if e != nil || string(payload) != strings.Repeat("protocol-payload", 4096) {
				t.Fatal("real TCP payload mismatch", e)
			}
			if c.TCPOnly {
				return
			}
			kind := strings.Split(c.Name, "-")[0]
			if udpDone[kind] {
				return
			}
			udpDone[kind] = true
			if c.UDPAlt != "" {
				g.UdpCheckDns = []string{c.UDPAlt}
			}
			r, e := ProbeRouterCoreUDP(ctx, c.Link, g)
			if e != nil || !r.Alive {
				t.Fatal("real UDP query failed", r, e)
			}
		})
	}
	// A supported node must still finish when a different imported protocol is
	// unavailable in Mihomo. No direct fallback may turn that failure green.
	var good string
	for _, c := range cases {
		if strings.HasPrefix(c.Name, "ss-") {
			good = c.Link
			break
		}
	}
	if good == "" {
		t.Fatal("missing SS fixture")
	}
	broken := ""
	for _, c := range cases {
		if c.Failure == "unreachable" {
			broken = c.Link
			break
		}
	}
	if broken == "" {
		t.Fatal("missing independent rejected-authentication fixture")
	}
	mixed, e := ProbeRouterCoreNodes(ctx, []string{good, broken}, global)
	if e != nil || len(mixed) != 2 || !mixed[0].Alive || mixed[1].Alive {
		t.Fatal("probe bypassed its authenticated node", mixed, e)
	}
	bulk, e := ProbeRouterCoreNodes(ctx, []string{good, good}, global)
	if e != nil || len(bulk) != 2 || !bulk[0].Alive || !bulk[1].Alive {
		t.Fatal("multi-node authenticated routing", bulk, e)
	}
	values, e := ProbeRouterCoreNodes(ctx, []string{"tor://default#not-supported", good, "ss://unsupported-cipher:fixture@127.0.0.1:1"}, global)
	if e != nil || len(values) != 3 || values[0].Alive || values[0].Message != "mihomo_protocol_unsupported" || !values[1].Alive || values[2].Alive || values[2].Message != "invalid_native_config" {
		t.Fatal("unsupported node blocked valid batch", values, e)
	}
}
func TestRouterMihomoDNSPreservesPolicyAndQuestion(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &dnsmessage.Server{PacketConn: packet, Net: "udp", Handler: dnsmessage.HandlerFunc(func(w dnsmessage.ResponseWriter, q *dnsmessage.Msg) {
		r := new(dnsmessage.Msg)
		r.SetReply(q)
		r.Answer = []dnsmessage.RR{&dnsmessage.A{Hdr: dnsmessage.RR_Header{Name: q.Question[0].Name, Rrtype: dnsmessage.TypeA, Class: dnsmessage.ClassINET, Ttl: 30}, A: net.IPv4(192, 0, 2, 40)}}
		_ = w.WriteMsg(r)
	})}
	go upstream.ActivateAndServe()
	defer upstream.Shutdown()
	port := packet.LocalAddr().(*net.UDPAddr).Port
	canonical := sbObject{"route": sbObject{"default_domain_resolver": sbObject{"server": "domestic"}, "rule_set": []sbObject{{"tag": "china", "rules": []sbObject{{"domain_suffix": []string{"cn"}}}}}}, "dns": sbObject{"servers": []sbObject{{"tag": "domestic", "type": "udp", "server": "127.0.0.1", "server_port": port}}, "final": "missing-no-fallback", "rules": []sbObject{{"rule_set": []string{"china"}, "action": "route", "server": "domestic"}, {"query_type": []string{"AAAA"}, "action": "predefined", "rcode": "NOERROR"}}}}
	profile := sbObject{"_nailink_dns": canonical, "dns": sbObject{}, "listeners": []sbObject{}}
	b, err := startMihomoDNS(profile, sbPorts{Secret: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	address := strings.TrimPrefix(mhStrings(profile["dns"].(sbObject)["nameserver"])[0], "udp://")
	for _, c := range []struct {
		name           string
		typ            uint16
		rcode, answers int
	}{{"www.example.cn.", dnsmessage.TypeA, 0, 1}, {"foreign.test.", dnsmessage.TypeA, dnsmessage.RcodeServerFailure, 0}, {"foreign.test.", dnsmessage.TypeAAAA, 0, 0}} {
		q := new(dnsmessage.Msg)
		q.SetQuestion(c.name, c.typ)
		a, _, err := (&dnsmessage.Client{Timeout: time.Second}).Exchange(q, address)
		if err != nil || !mhValidDNSAnswer(q, a) || a.Rcode != c.rcode || len(a.Answer) != c.answers {
			t.Fatal("DNS policy/answer changed", c.name, a, err)
		}
	}
	// Boolean request rules and negation are evaluated in their original order.
	f, err := mhDNSCompile(sbObject{"type": "logical", "mode": "and", "rules": []sbObject{{"domain_suffix": []string{"example.com"}}, {"query_type": []string{"AAAA"}, "invert": true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := new(dnsmessage.Msg)
	q.SetQuestion("sub.example.com.", dnsmessage.TypeA)
	if !f(q) {
		t.Fatal("lost logical DNS match")
	}
	q.SetQuestion("sub.example.com.", dnsmessage.TypeAAAA)
	if f(q) {
		t.Fatal("lost negation")
	}
}
func TestRouterMihomoSessionLifecycle(t *testing.T) {
	binary := os.Getenv("DAED_TEST_MIHOMO")
	if binary == "" {
		t.Skip("fixed optional Mihomo required")
	}
	oldBinary, oldDir := routerMihomoBinary, routerCoreRuntimeDir
	routerMihomoBinary, routerCoreRuntimeDir = binary, filepath.Join(t.TempDir(), "runtime")
	defer func() { routerMihomoBinary, routerCoreRuntimeDir = oldBinary, oldDir }()
	conf, dirs := coreFixtureConfig(t)
	conf.Global.WanInterface = []string{"lo"}
	conf.Global.TcpCheckUrl = []string{"http://127.0.0.1:9"}
	for i := range conf.Group {
		conf.Group[i].Policy = "fixed"
	}
	conf.Dns.Upstream = []daeConfig.KeyableString{"test:udp://127.0.0.1:9"}
	conf.Dns.Routing.Request.Rules = nil
	conf.Dns.Routing.Request.Fallback = "test"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := newRouterNativeSession(ctx, logrus.New(), conf, dirs, false, "mihomo")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	native := s.(*routerSingBoxSession)
	if native.engine != "mihomo" || native.dnsBridge == nil {
		t.Fatal("not a managed native session")
	}
	if err = s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(native.dir); !os.IsNotExist(err) {
		t.Fatal("private runtime directory survived stop")
	}
}

func TestRouterMihomoStartupRestoresPreviousNativeCore(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	before := restartConfigKey(conf)
	live := 0
	m := &routerCoreManager{root: dirs[0], conf: conf, dirs: dirs}
	m.factory = func(_ context.Context, name string, _ *daeConfig.Config) (routerCoreSession, error) {
		if name == "mihomo" {
			return nil, fmt.Errorf("mihomo_version_mismatch")
		}
		if name != "sing-box" {
			t.Fatal("did not prefer previous working native core", name)
		}
		live++
		return &coreFakeSession{live: &live, done: make(chan error)}, nil
	}
	_, state, err := m.startInitial(routercore.State{Current: "mihomo", Previous: "sing-box", Phase: "stable", Running: true}, conf)
	if err != nil || state.Current != "sing-box" || live != 1 || before != restartConfigKey(conf) {
		t.Fatal("startup recovery changed configuration or selected wrong core", state, err)
	}
}

func TestRouterMihomoMainWANReadOnly(t *testing.T) {
	if os.Getenv("DAED_TEST_MIHOMO") == "" {
		t.Skip("native integration environment required")
	}
	iface, e := routerMainWANInterface()
	if e != nil || iface == "" || iface == routercore.TunName {
		t.Fatal(iface, e)
	}
}
