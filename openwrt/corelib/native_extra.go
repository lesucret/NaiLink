// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// WireGuard stays in a userspace endpoint. Imported nodes cannot create host
// interfaces/listeners or select executable paths for optional clients.
func validateExtraNode(node Object, kind string) (bool, error) {
	switch kind {
	case "naive":
		if concurrency, exists := node["insecure_concurrency"]; exists {
			n, ok := concurrency.(float64)
			if !ok || n < 0 || n > 4 || n != float64(int(n)) {
				return false, errors.New("invalid_naive_concurrency")
			}
		}
		if tls, ok := node["tls"].(map[string]any); ok {
			if tls["insecure"] == true {
				return false, errors.New("unsupported_naive_insecure")
			}
		}
		return false, nil
	case "wireguard":
		for key := range node {
			for _, canonical := range []string{"system", "name", "listen_port", "workers", "mtu", "peers", "address", "private_key"} {
				if strings.EqualFold(key, canonical) && key != canonical {
					return true, errors.New("unsupported_native_field")
				}
			}
		}
		if system, exists := node["system"]; exists {
			v, ok := system.(bool)
			if !ok || v {
				return true, errors.New("unsupported_wireguard_system")
			}
		}
		if value, exists := node["name"]; exists {
			name, ok := value.(string)
			if !ok || name != "" {
				return true, errors.New("unsupported_wireguard_system")
			}
		}
		if value, exists := node["listen_port"]; exists {
			port, ok := value.(float64)
			if !ok || port != 0 {
				return true, errors.New("unsupported_wireguard_system")
			}
		}
		if !wireguardKey(node["private_key"]) {
			return true, errors.New("invalid_wireguard_key")
		}
		addresses := nativeStrings(node["address"])
		if len(addresses) == 0 || len(addresses) > 8 {
			return true, errors.New("invalid_wireguard_address")
		}
		for _, address := range addresses {
			if _, err := netip.ParsePrefix(address); err != nil {
				return true, errors.New("invalid_wireguard_address")
			}
		}
		peers, ok := node["peers"].([]any)
		if !ok || len(peers) == 0 || len(peers) > 8 {
			return true, errors.New("invalid_wireguard_peer")
		}
		for _, entry := range peers {
			peer, ok := entry.(map[string]any)
			if !ok {
				return true, errors.New("invalid_wireguard_peer")
			}
			address, _ := peer["address"].(string)
			port, _ := peer["port"].(float64)
			if address == "" || strings.ContainsAny(address, " /\\\x00\r\n\t@") || port < 1 || port > 65535 || port != float64(int(port)) || !wireguardKey(peer["public_key"]) {
				return true, errors.New("invalid_wireguard_peer")
			}
			if key, exists := peer["pre_shared_key"]; exists {
				s, ok := key.(string)
				if !ok || s != "" && !wireguardKey(key) {
					return true, errors.New("invalid_wireguard_key")
				}
			}
			prefixes := nativeStrings(peer["allowed_ips"])
			if len(prefixes) == 0 || len(prefixes) > 64 {
				return true, errors.New("invalid_wireguard_peer")
			}
			for _, prefix := range prefixes {
				if _, err := netip.ParsePrefix(prefix); err != nil {
					return true, errors.New("invalid_wireguard_peer")
				}
			}
		}
		workers, exists := node["workers"]
		if !exists {
			node["workers"] = 1
		} else if n, ok := workers.(float64); !ok || n < 1 || n > 4 || n != float64(int(n)) {
			return true, errors.New("invalid_wireguard_workers")
		}
		if mtu, exists := node["mtu"]; exists {
			n, ok := mtu.(float64)
			if !ok || n < 576 || n > 9000 || n != float64(int(n)) {
				return true, errors.New("invalid_wireguard_mtu")
			}
		}
		node["system"] = false
		return true, nil
	case "tor":
		for key := range node {
			if key != "type" && key != "tag" && key != "detour" && key != "torrc" {
				return true, errors.New("unsupported_tor_option")
			}
		}
		if options, exists := node["torrc"]; exists {
			values, ok := options.(map[string]any)
			if !ok {
				return true, errors.New("unsupported_tor_option")
			}
			for key, value := range values {
				if key != "ExitNodes" && key != "ExcludeNodes" && key != "ExcludeExitNodes" && key != "StrictNodes" {
					return true, errors.New("unsupported_tor_option")
				}
				s, ok := value.(string)
				if !ok || len(s) > 2048 || key == "StrictNodes" && s != "0" && s != "1" || key != "StrictNodes" && !torExitPattern.MatchString(s) {
					return true, errors.New("unsupported_tor_option")
				}
			}
		}
		return true, nil
	}
	return false, nil
}

var torExitPattern = regexp.MustCompile(`^[A-Za-z0-9${},.~/:-]*$`)

func nativeStrings(value any) []string {
	if s, ok := value.(string); ok {
		return []string{s}
	}
	var result []string
	if list, ok := value.([]any); ok {
		for _, value := range list {
			s, ok := value.(string)
			if !ok {
				return nil
			}
			result = append(result, s)
		}
	}
	return result
}
func wireguardKey(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(key) == 32
}

func nativeExtraMetadata(node Object) (string, string, bool) {
	kind, _ := node["type"].(string)
	if kind == "tor" {
		return "Tor 网络", "tor", true
	}
	if kind == "wireguard" {
		peers := node["peers"].([]any)
		peer := peers[0].(map[string]any)
		return net.JoinHostPort(peer["address"].(string), strconv.Itoa(int(peer["port"].(float64)))), kind, true
	}
	return "", "", false
}

func NativeFeature(node Object) string {
	kind, _ := node["type"].(string)
	if kind == "naive" || kind == "wireguard" || kind == "tor" {
		return kind
	}
	return ""
}

func naiveNode(link, tag string) (Object, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "naive+https" || u.Hostname() == "" {
		return nil, errors.New("invalid_node")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("unsupported_node_path")
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid_node")
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid_node")
	}
	for key, values := range q {
		if key != "sni" || len(values) != 1 {
			return nil, OptionProblem("unsupported_node_option", key)
		}
	}
	tls := Object{"enabled": true}
	if q.Get("sni") != "" {
		tls["server_name"] = q.Get("sni")
	}
	node := Object{"type": "naive", "tag": tag, "server": u.Hostname(), "server_port": port, "tls": tls, "stream_receive_window": "8MB"}
	if u.User != nil {
		node["username"] = u.User.Username()
		node["password"], _ = u.User.Password()
	}
	return node, nil
}

func torURI(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "tor" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Path != "" && u.Path != "/" || u.Host != "" && u.Host != "default" {
		return "", errors.New("invalid_node")
	}
	name := u.Fragment
	if name == "" {
		name = "Tor"
	}
	return NativeLink(`{"type":"tor","tag":` + strconv.Quote(name) + `}`)
}

func extraSourceIdentity(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
