package routercore

import "testing"

func TestMihomoPluginFlagsAndSNI(t *testing.T) {
	o, e := mihomoPlugin("v2ray-plugin", "tls;host=example.com;mux=false")
	if e != nil || o["tls"] != true || o["mux"] != false || o["host"] != "example.com" {
		t.Fatal(o, e)
	}
	o, e = mihomoPlugin("obfs-local", "obfs=tls;obfs-host=example.com")
	if e != nil || o["mode"] != "tls" || o["host"] != "example.com" {
		t.Fatal(o, e)
	}
	if _, e = mihomoPlugin("v2ray-plugin", "tls;unknown=value"); e == nil {
		t.Fatal("unknown plugin option ignored")
	}
	node := Object{"type": "http", "tag": "test", "server": "127.0.0.1", "server_port": 443, "tls": Object{"enabled": true, "server_name": "example.com"}}
	o, e = MihomoNode(node)
	if e != nil || o["sni"] != "example.com" {
		t.Fatal(o, e)
	}
	node["connect_timeout"] = "5s"
	if _, e = MihomoNode(node); e == nil {
		t.Fatal("unmapped option ignored")
	}
}

func TestMihomoProtocolDurationUnits(t *testing.T) {
	base := func(kind string) Object {
		return Object{"type": kind, "tag": "test", "server": "127.0.0.1", "server_port": 443}
	}
	n := base("tuic")
	n["heartbeat"] = "15s"
	o, e := MihomoNode(n)
	if e != nil || o["heartbeat-interval"] != 15000 {
		t.Fatal(o, e)
	}
	n = base("hysteria2")
	n["hop_interval"] = "30s"
	o, e = MihomoNode(n)
	if e != nil || o["hop-interval"] != "30" {
		t.Fatal(o, e)
	}
}

func TestMihomoRealityPreservesNativeKeyShare(t *testing.T) {
	n := Object{"type": "vless", "tag": "test", "server": "127.0.0.1", "server_port": 443, "tls": Object{"enabled": true, "reality": Object{"enabled": true, "public_key": "fixture", "short_id": "abcd"}, "utls": Object{"enabled": true, "fingerprint": "chrome"}}}
	o, e := MihomoNode(n)
	if e != nil || o["reality-opts"].(Object)["support-x25519mlkem768"] != true {
		t.Fatal(o, e)
	}
}
