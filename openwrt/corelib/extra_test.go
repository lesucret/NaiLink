package routercore

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extraFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	dir := filepath.Join(root, "extra-fixture")
	_ = os.Mkdir(dir, 0700)
	for _, name := range []string{"sing-box", "tor-launch", "tor/bin/tor"} {
		path := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		_ = os.WriteFile(path, []byte("fixture"), 0700)
	}
	if err := os.Symlink("extra-fixture", filepath.Join(root, "extra")); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestExtraNodesPreserveSourceAndSeparateEndpoint(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	raw := `{"type":"wireguard","tag":"wg","private_key":"` + key + `","address":["10.0.0.2/32"],"peers":[{"address":"example.com","port":51820,"public_key":"` + key + `","allowed_ips":["0.0.0.0/0"]}]}`
	link, err := NativeLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Node(link, "node:wg", NodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	profile := Object{"outbounds": ExpandNode(node)}
	required, tor, err := PrepareExtraProfile(profile, extraFixture(t), t.TempDir(), "main")
	if err != nil || !required || tor || len(profile["outbounds"].([]Object)) != 0 || profile["endpoints"].([]Object)[0]["workers"] != 1 {
		t.Fatal(profile, err)
	}
	for _, field := range []string{`"system":true,`, `"System":true,`, `"listen_port":51820,`, `"workers":99,`, `"name":"wg-host",`} {
		invalid := strings.Replace(raw, `"type":"wireguard",`, `"type":"wireguard",`+field, 1)
		if _, err := NativeLink(invalid); err == nil {
			t.Fatal("unsafe endpoint accepted", field)
		}
	}
}
func TestManagedTorPathsAndResourceLimit(t *testing.T) {
	for _, raw := range []string{`{"type":"tor","executable_path":"/bin/sh"}`, `{"type":"tor","extra_args":["--run"]}`, `{"type":"tor","torrc":{"DataDirectory":"/root"}}`, `{"type":"tor","torrc":{"ExitNodes":"us\nRunAsDaemon 1"}}`} {
		if _, err := NativeLink(raw); err == nil {
			t.Fatal("unmanaged Tor options accepted")
		}
	}
	link, err := EncodeNativeURI("tor://default#Tor")
	if err != nil {
		t.Fatal(err)
	}
	n, err := Node(link, "node:tor", NodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	profile := Object{"outbounds": ExpandNode(n)}
	data := t.TempDir()
	required, tor, err := PrepareExtraProfile(profile, extraFixture(t), data, "probe")
	if err != nil || !required || !tor {
		t.Fatal(err)
	}
	node := profile["outbounds"].([]Object)[0]
	encoded, _ := json.Marshal(profile)
	if strings.Contains(string(encoded), "_nailink") || !strings.HasPrefix(node["data_directory"].(string), data+"/") || node["torrc"].(map[string]any)["MaxMemInQueues"] != "64 MB" {
		t.Fatal("unmanaged Tor profile")
	}
}
func TestNaiveUsesBoundedWindowAndDoesNotWeakenTLS(t *testing.T) {
	node, err := Node("naive+https://user:password@example.com:443#name", "node:naive", NodeOptions{AllowInsecure: true})
	if err != nil || node["stream_receive_window"] != "8MB" || node["tls"].(Object)["insecure"] != nil {
		t.Fatal(node, err)
	}
	if IsNativeURI("naive+https://user:password@example.com") {
		t.Fatal("broke dae Naive import")
	}
	if _, err := Node("naive+https://user:password@example.com?insecure=1", "node:n", NodeOptions{}); err == nil {
		t.Fatal("silently dropped certificate option")
	}
}
func TestExtraDirectoryRejectsOutsideLink(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	_ = os.Symlink(t.TempDir(), filepath.Join(root, "extra"))
	if _, err := ExtraDirectory(root); err == nil {
		t.Fatal("external component directory accepted")
	}
}

func TestNativeBundleDoesNotStartUnreferencedComponents(t *testing.T) {
	raw := `{"tag":"main","outbounds":[{"type":"socks","tag":"main","server":"example.com","server_port":1080},{"type":"naive","tag":"unused","server":"example.com","server_port":443,"tls":{"enabled":true}},{"type":"tor","tag":"unused-tor"}]}`
	link, err := NativeLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Node(link, "node:main", NodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active := ExpandNode(node)
	if len(active) != 1 || active[0]["type"] != "socks" {
		t.Fatal("unused engines were activated")
	}
	decoded, err := decode64(strings.TrimPrefix(link, "singbox://"))
	if err != nil || string(decoded) != raw {
		t.Fatal("original JSON changed")
	}
}
