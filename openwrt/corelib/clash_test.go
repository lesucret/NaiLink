// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"strings"
	"testing"
)

func TestClashCommonNodesPreserveAuthenticationAndTransport(t *testing.T) {
	for _, kind := range []string{"ss", "vmess", "vless", "trojan", "hysteria2", "tuic", "anytls", "socks5", "http"} {
		t.Run(kind, func(t *testing.T) {
			p := Object{"name": "香港 01 / 测试", "type": kind, "server": "2001:db8::42", "port": 443, "udp": true}
			switch kind {
			case "ss":
				p["password"], p["cipher"] = "test:p@ss/+", "chacha20-ietf-poly1305"
			case "vmess", "vless":
				p["uuid"] = "11111111-1111-4111-8111-111111111111"
				p["tls"] = true
				p["skip-cert-verify"] = false
				p["servername"] = "fixture.test"
				p["network"] = "ws"
				p["ws-opts"] = Object{"path": "/ws?ed=1024", "headers": Object{"Host": "fixture.test"}}
			case "trojan", "hysteria2", "anytls":
				p["password"] = "test:p@ss/+"
				p["sni"] = "fixture.test"
				p["skip-cert-verify"] = false
			case "tuic":
				p["password"], p["uuid"] = "fixture", "11111111-1111-4111-8111-111111111111"
				p["sni"] = "fixture.test"
			case "socks5", "http":
				p["username"], p["password"] = "u@ser", "test:p@ss/+"
			}
			link, e := ClashLink(p)
			if e != nil {
				t.Fatal(e)
			}
			n, e := Node(link, "fixture", NodeOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if n["server"] != "2001:db8::42" || intValue(n["server_port"]) != 443 {
				t.Fatal("server changed")
			}
			if password, ok := p["password"]; ok && n["password"] != password {
				t.Fatal("password was changed")
			}
			if kind == "vmess" || kind == "vless" {
				transport := n["transport"].(Object)
				tls := n["tls"].(Object)
				if transport["path"] != "/ws?ed=1024" || transport["headers"].(Object)["Host"] != "fixture.test" || tls["insecure"] != false {
					t.Fatal("transport or certificate setting changed")
				}
			}
			if _, e = MihomoNode(n); e != nil {
				t.Fatal("Mihomo could not consume converted node", e)
			}
		})
	}
}

func TestClashConflictsAndUnsupportedOptionsNeverDisappear(t *testing.T) {
	base := func() Object {
		return Object{"name": "fixture", "type": "vmess", "server": "fixture.invalid", "port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "tls": true}
	}
	for _, patch := range []Object{
		{"network": "ws", "ws-path": "/old", "ws-opts": Object{"path": "/new"}},
		{"network": "ws", "ws-opts": Object{"headers": Object{"Authorization": "secret"}}},
		{"skip-cert-verify": "true"}, {"alterId": 32}, {"certificate": "/etc/secret"},
		{"dialer-proxy": "outside"}, {"plugin": "unknown"},
	} {
		p := base()
		for k, v := range patch {
			p[k] = v
		}
		_, e := ClashLink(p)
		if e == nil {
			t.Fatal("unsupported option was silently ignored")
		}
		if strings.Contains(DescribeError(e), "/etc/secret") || strings.Contains(DescribeError(e), "Authorization: secret") {
			t.Fatal("option values leaked")
		}
	}
}

func TestClashLegacyWebsocketAliasesAndUDPFalse(t *testing.T) {
	p := Object{"name": "fixture", "type": "vmess", "server": "fixture.invalid", "port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "network": "ws", "tls": true, "udp": false, "ws-path": "/ws", "ws-headers": Object{"Host": "fixture.test"}, "ws-opts": Object{"path": "/ws", "headers": Object{"Host": "fixture.test"}}}
	link, e := ClashLink(p)
	if e != nil {
		t.Fatal(e)
	}
	n, e := Node(link, "fixture", NodeOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if n["network"] != "tcp" || n["transport"].(Object)["path"] != "/ws" {
		t.Fatal("UDP or legacy WebSocket settings changed")
	}
}
