// SPDX-License-Identifier: AGPL-3.0-only
package subscription

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/routercore"
	"gopkg.in/yaml.v3"
)

const clashFixture = `mixed-port: 7890
external-controller: 0.0.0.0:9090
dns: {nameserver: [https://dns.fixture.invalid/dns-query]}
rules: [MATCH,proxy]
proxies:
  - {name: 香港 Trojan, type: trojan, server: 192.0.2.10, port: 443, password: 'test:p@ss/+', sni: fixture.test, udp: true, skip-cert-verify: false}
  - name: VMess WS
    type: vmess
    server: fixture.test
    port: 443
    uuid: 11111111-1111-4111-8111-111111111111
    alterId: 0
    cipher: auto
    tls: true
    udp: true
    skip-cert-verify: false
    network: ws
    ws-path: /ws
    ws-headers: {Host: fixture.test}
    ws-opts: {path: /ws, headers: {Host: fixture.test}}
`

func TestClashConvertedURIsKeepLegacyNodeMetadata(t *testing.T) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if e := yaml.Unmarshal([]byte(clashFixture), &doc); e != nil {
		t.Fatal(e)
	}
	for i, p := range doc.Proxies {
		link, e := routercore.ClashLink(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.NewNodeModel(link, nil, nil); e != nil {
			t.Fatalf("fixture %d metadata: %v", i, e)
		}
	}
}

func TestClashSubscriptionOnlyImportsProxies(t *testing.T) {
	for _, payload := range []string{clashFixture, "\ufeff" + clashFixture, base64.StdEncoding.EncodeToString([]byte(clashFixture)), base64.RawURLEncoding.EncodeToString([]byte(clashFixture))} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e != nil {
			t.Fatal(e)
		}
		if len(links) != 2 {
			t.Fatal("DNS/controller URLs were imported or proxies lost", len(links))
		}
		for _, link := range links {
			if strings.HasPrefix(link, "http") {
				t.Fatal("imported configuration URL")
			}
			if _, e := routercore.Node(link, "fixture", routercore.NodeOptions{}); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func TestSubscriptionRejectsStructuredErrorsAndPartialUpdates(t *testing.T) {
	for _, payload := range []string{
		`<html><a href="https://login.fixture.invalid">login</a></html>`,
		`{"error":"https://private-token.fixture.invalid/login"}`,
		"proxy-providers:\n  external: {type: http, url: 'https://private-token.fixture.invalid/other'}",
		"proxies: []\nproxies: []",
		"proxies: []\n---\nproxies: []",
		"proxies: &cycle [*cycle]",
		clashFixture + "  - {name: unsupported, type: unknown, server: fixture.test, port: 443, password: private-token}",
		"https://private-token.fixture.invalid/sub?key=private-token",
		fixtureNode + "\nunknown://user:private-token@fixture.invalid:443",
	} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e == nil || len(links) != 0 {
			t.Fatal("invalid/partial subscription accepted")
		}
		if strings.Contains(e.Error(), "private-token") {
			t.Fatal("credentials leaked")
		}
		if !isSubscriptionContentError(e) {
			t.Fatal("content failure would trigger pointless network retries")
		}
	}
}
func TestSubscriptionPlainAndBase64URIsRemainCompatible(t *testing.T) {
	for _, payload := range []string{fixtureNode, base64.StdEncoding.EncodeToString([]byte(fixtureNode)), "# comment\n" + fixtureNode + "\n" + fixtureNode} {
		links, e := decodeSubscriptionPayload([]byte(payload))
		if e != nil || len(links) != 1 || links[0] != fixtureNode {
			t.Fatal("ordinary subscription regression", e)
		}
	}
}
func TestClashSubscriptionAliasAndMerge(t *testing.T) {
	payload := `defaults: &defaults {type: trojan, server: 192.0.2.10, port: 443, password: fixture}
proxies:
  - <<: *defaults
    name: first
  - <<: *defaults
    name: second
`
	links, e := decodeSubscriptionPayload([]byte(payload))
	if e != nil || len(links) != 2 {
		t.Fatal("safe YAML aliases regressed", e)
	}
}
func TestClashCapturedProviderDocument(t *testing.T) {
	path := os.Getenv("DAED_PRIVATE_CLASH_CAPTURE")
	if path == "" {
		t.Skip("private provider capture is never included in source")
	}
	payload, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	links, e := decodeSubscriptionPayload(payload)
	if e != nil {
		t.Fatal(e)
	}
	if len(links) != 70 {
		t.Fatal("provider node count mismatch", len(links))
	}
	for _, link := range links {
		if _, e := routercore.Node(link, "private", routercore.NodeOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("private 70-node Clash document parsed without importing global settings")
}
