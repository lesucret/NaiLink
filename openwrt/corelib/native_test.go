// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeBundleKeepsAuthenticationAndPrivateDetours(t *testing.T) {
	value := `{"tag":"inner","outbounds":[{"type":"shadowsocks","tag":"inner","server":"example.com","server_port":443,"method":"aes-128-gcm","password":"secret","detour":"outer"},{"type":"shadowtls","tag":"outer","server":"example.com","server_port":443,"version":3,"password":"another","tls":{"enabled":true,"server_name":"example.com","insecure":false}}]}`
	link, err := NativeLink(value)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Node(link, "node:test", NodeOptions{AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	all := ExpandNode(node)
	if len(all) != 2 || all[0]["tag"] != "node:test" || all[0]["detour"] != "node:test/out-1" || all[1]["tag"] != "node:test/out-1" || all[0]["password"] != "secret" {
		t.Fatal("lost private dependency or authentication")
	}
	if all[1]["tls"].(map[string]any)["insecure"] != false {
		t.Fatal("weakened explicit certificate validation")
	}
	for _, n := range all {
		for k := range n {
			if strings.HasPrefix(k, "_nailink") {
				t.Fatal("internal metadata reached native config")
			}
		}
	}
}
func TestNativeRejectsSystemAccessExternalDetoursAndCycles(t *testing.T) {
	cases := []string{
		`{"type":"ssh","server":"example.com","server_port":22,"private_key_path":"/root/private"}`,
		`{"type":"ssh","server":"example.com","server_port":22,"Private_Key_Path":"/root/private"}`,
		`{"type":"hysteria2","server":"example.com","server_port":443,"tls":{"enabled":true,"ech":{"config_path":"/root/private"}}}`,
		`{"type":"ssh","server":"example.com","server_port":22,"protect_path":"/tmp/unix-socket"}`,
		`{"type":"shadowsocks","server":"example.com","server_port":443,"plugin":{}}`,
		`{"type":"ssh","server":"example.com","server_port":22,"detour":"direct"}`,
		`{"type":"ssh","tag":"a","server":"example.com","server_port":22,"detour":"a"}`,
		`{"outbounds":[{"type":"ssh","server":"example.com","server_port":22}],"inbounds":[]}`,
		`{"type":"wireguard","server":"example.com","server_port":443}`,
		`{"type":"tor","server":"example.com","server_port":443}`,
		`{"type":"shadowtls","server":"example.com","server_port":443}`,
	}
	for _, value := range cases {
		if _, err := NativeLink(value); err == nil {
			t.Fatal("accepted unsafe/unavailable native option", value)
		}
	}
}
func TestNativeURIProtocolMappings(t *testing.T) {
	for _, uri := range []string{"hysteria://auth@example.com:443?peer=example.com&upmbps=20&downmbps=100", "ssh://user:password@example.com:22?host_key=key", "snell://psk@example.com:443?version=6"} {
		link, err := EncodeNativeURI(uri)
		if err != nil {
			t.Fatal(err)
		}
		n, err := Node(link, "node:test", NodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(n)
		if strings.Contains(string(data), "_path") {
			t.Fatal("unexpected file access")
		}
	}
	for _, uri := range []string{"ssh://user:password@example.com:22?private_key_path=/root/key", "snell://psk@example.com:443?version=3", "hysteria://auth@example.com:443?insecure=yes"} {
		if _, err := EncodeNativeURI(uri); err == nil {
			t.Fatal("ignored unsupported option", uri)
		}
	}
}
