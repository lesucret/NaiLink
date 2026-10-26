// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

func coreFixtureConfig(t *testing.T) (*daeConfig.Config, []string) {
	t.Helper()
	root := t.TempDir()
	ipdata := &geodata.GeoIPList{Entry: []*geodata.GeoIP{{CountryCode: "private", Cidr: []*geodata.CIDR{{Ip: []byte{10, 0, 0, 0}, Prefix: 8}}}, {CountryCode: "cn", Cidr: []*geodata.CIDR{{Ip: []byte{1, 2, 3, 0}, Prefix: 24}}}}}
	domains := &geodata.GeoSiteList{Entry: []*geodata.GeoSite{{CountryCode: "cn", Domain: []*geodata.Domain{{Type: geodata.Domain_RootDomain, Value: "cn"}}}}}
	for name, value := range map[string]proto.Message{"geoip.dat": ipdata, "geosite.dat": domains} {
		data, err := proto.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	text := `global { lan_interface: lan0
 wan_interface: auto
 check_interval: 30s
 tcp_check_url: 'http://cp.cloudflare.com'
 }
 node { fixture: 'socks5://127.0.0.1:1080' }
 group { proxy { filter: name(fixture)
 policy: min_moving_avg
 } }
 dns { upstream { alidns: 'udp://223.5.5.5:53'
 googledns: 'tcp+udp://8.8.8.8:53'
 }
 routing { request { qname(geosite:cn) -> alidns
 fallback: googledns
 } } }
 routing { pname(NetworkManager, systemd-resolved, dnsmasq) -> must_direct
 dip(geoip:private) -> direct
 dip(geoip:cn) -> direct
 domain(geosite:cn) -> direct
 fallback: proxy
 }`
	sections, err := config_parser.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := daeConfig.New(sections)
	if err != nil {
		t.Fatal(err)
	}
	return conf, []string{root}
}
func TestRouterCoreDefaultConfigurationConversion(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	original := restartConfigKey(conf)
	profile, warnings, err := singBoxConfig(conf, dirs, sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "fixture"}, true)
	if err != nil {
		for _, rule := range conf.Routing.Rules {
			t.Logf("outbound: %#v", rule.Outbound)
		}
		t.Fatal(err)
	}
	if len(warnings) < 2 {
		t.Fatal("lost semantic differences")
	}
	if len(profile["inbounds"].([]sbObject)) != 3 {
		t.Fatal("missing TUN or private management listeners")
	}
	if original != restartConfigKey(conf) {
		t.Fatal("changed source config")
	}
	binary := os.Getenv("DAED_TEST_SINGBOX")
	if binary == "" {
		t.Skip("set DAED_TEST_SINGBOX for real native configuration validation")
	}
	oldBinary, oldRuntime := routerSingBoxBinary, routerCoreRuntimeDir
	routerSingBoxBinary = binary
	routerCoreRuntimeDir = t.TempDir()
	if err := os.Chmod(routerCoreRuntimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { routerSingBoxBinary = oldBinary; routerCoreRuntimeDir = oldRuntime })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = routerCheckSingBox(ctx, conf, dirs, true); err != nil {
		path := filepath.Join(t.TempDir(), "config.json")
		data, _ := json.Marshal(profile)
		_ = os.WriteFile(path, data, 0600)
		output, _ := exec.CommandContext(ctx, binary, "check", "--disable-color", "-c", path).CombinedOutput()
		t.Fatalf("%v: %s", err, output)
	}
}
func TestRouterCoreUnsupportedConfigNeverFallsBackToDirect(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	conf.Global.Mptcp = true
	if _, _, err := singBoxConfig(conf, dirs, sbPorts{}, true); err == nil {
		t.Fatal("silently ignored MPTCP")
	}
	conf.Global.Mptcp = false
	conf.Routing.Rules = append(conf.Routing.Rules, &config_parser.RoutingRule{AndFunctions: []*config_parser.Function{{Name: "mac", Params: []*config_parser.Param{{Val: "00:11:22:33:44:55"}}}}, Outbound: config_parser.Function{Name: "direct"}})
	if _, _, err := singBoxConfig(conf, dirs, sbPorts{}, true); err == nil {
		t.Fatal("silently ignored routing constraint")
	}
}
