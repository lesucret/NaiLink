// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestHY2PortRangesAreNativeAndValidated(t *testing.T) {
	out, err := Node("hy2://secret@example.test:443?mport=443-445,8443", "fixture", NodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out["server_ports"].([]string), ","); got != "443:445,8443:8443" {
		t.Fatal(got)
	}
	for _, value := range []string{"0", "65536", "445-443", "443--445", "443,", "invalid"} {
		if _, err := Node("hy2://secret@example.test:443?ports="+value, "fixture", NodeOptions{}); err == nil {
			t.Fatal("accepted invalid port range", value)
		}
	}
}

func TestNodeCredentialAndTransportPreservation(t *testing.T) {
	cases := []struct{ link, kind string }{
		{"ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:p@ss:word")) + "@[::1]:8443#label", "shadowsocks"},
		{"ss://2022-blake3-aes-128-gcm:c2VjcmV0@127.0.0.1:8443", "shadowsocks"},
		{"vless://00000000-0000-4000-8000-000000000001@example.test:443?security=reality&pbk=publickey&sid=1234&fp=chrome&flow=xtls-rprx-vision", "vless"},
		{"trojan://p%40ss%3Aword@example.test:443?type=ws&host=cdn.test&path=%2Fws&sni=cert.test&insecure=0", "trojan"},
		{"hy2://p%40ss@example.test:443?obfs=salamander&obfs-password=abc&mport=443-445,8443&sni=cert.test&insecure=1", "hysteria2"},
		{"anytls://p%40ss@example.test:443/?insecure=1&sni=cert.test", "anytls"},
		{"tuic://00000000-0000-4000-8000-000000000001:secret@example.test:443?congestion_control=bbr&udp_relay_mode=native", "tuic"},
		{"socks5://user:pass@[::1]:1080", "socks"},
		{"https://user:pass@example.test:443?sni=cert.test", "http"},
	}
	for _, item := range cases {
		out, err := Node(item.link, "test", NodeOptions{})
		if err != nil {
			t.Fatalf("%s: %v", item.kind, err)
		}
		if out["type"] != item.kind {
			t.Fatal(out)
		}
		if out["tag"] != "test" {
			t.Fatal("lost identity")
		}
	}
	out, err := Node(cases[3].link, "test", NodeOptions{AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if out["password"] != "p@ss:word" || out["tls"].(Object)["insecure"] != false {
		t.Fatal("credential or explicit certificate choice changed")
	}
}
func TestNodeRefusesUnknownOptionsAndUnsupportedTransports(t *testing.T) {
	for _, link := range []string{
		"anytls://secret@example.test:443?future-option=1",
		"vless://uuid@example.test:443?type=xhttp",
		"vless://uuid@example.test:443?type=grpc&authority=custom",
		"vless://uuid@example.test:443?encryption=some-new-extension",
		"hy2://secret@example.test:0",
		"ssr://encoded",
		"anytls://secret@example.test:443?insecure=1&insecure=0",
		"anytls://secret@example.test:443?sni=invalid%ZZ",
	} {
		if _, err := Node(link, "test", NodeOptions{}); err == nil {
			t.Fatalf("silently accepted %s", link)
		}
	}
}

func TestExporterCompatibilityPreservesSecurityAndUDP(t *testing.T) {
	var warnings []string
	link := "vless://00000000-0000-4000-8000-000000000001@example.test:443?security=reality&pbk=fixture&sid=abcd&fp=chrome&flow=xtls-rprx-vision&type=tcp&headerType=none&spx=%2Ffixture-crawl"
	out, err := Node(link, "fixture", NodeOptions{Warn: func(s string) { warnings = append(warnings, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if out["flow"] != "xtls-rprx-vision" || out["tls"].(Object)["reality"].(Object)["public_key"] != "fixture" || len(warnings) != 1 || !strings.Contains(warnings[0], "spx") {
		t.Fatal(out, warnings)
	}
	for _, flag := range []string{"0", "false", "1", "true"} {
		out, err = Node("trojan://fixture@example.test:443?peer=cert.test&sni=cert.test&allowInsecure=1&udp="+flag, "fixture", NodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if (flag == "0" || flag == "false") && out["network"] != "tcp" {
			t.Fatal(out)
		}
		if (flag == "1" || flag == "true") && out["network"] != nil {
			t.Fatal("disabled native UDP", out)
		}
		if out["tls"].(Object)["insecure"] != true || out["password"] != "fixture" {
			t.Fatal("changed authentication", out)
		}
	}
	for _, suffix := range []string{"&headerType=http", "&udp=maybe", "&spx=https%3A%2F%2Fbad.example", "&spx=%2Fbad%0Apath"} {
		if _, err = Node("vless://uuid@example.test:443?security=reality&pbk=fixture"+suffix, "fixture", NodeOptions{}); err == nil {
			t.Fatal("accepted unsupported behavior", suffix)
		}
	}
}

func TestNodeDiagnosticsAreDeterministicAndNeverContainValues(t *testing.T) {
	_, err := Node("trojan://secret@example.test:443?zzz=private-value&aaa=other-secret", "fixture", NodeOptions{})
	message := DescribeError(err)
	if !strings.Contains(message, "aaa、zzz") || strings.Contains(message, "secret") || strings.Contains(message, "unsupported_") {
		t.Fatal(message)
	}
	if got := DescribeError(OptionProblem("unsupported_node_option", "bad\ncredential-value")); strings.Contains(got, "credential") {
		t.Fatal(got)
	}
}
