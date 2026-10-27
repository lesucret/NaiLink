// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
)

var nativeProxyTypes = map[string]bool{"socks": true, "http": true, "shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "hysteria": true, "hysteria2": true, "tuic": true, "anytls": true, "ssh": true, "snell": true, "shadowtls": true, "naive": true, "wireguard": true, "tor": true}

// Native outbounds stay inside a node's private namespace. They cannot add
// listeners, routes, services, local file reads, commands or external detours.
func NativeNode(link, tag string) (Object, error) {
	if len(link) > 180000 || tag == "" {
		return nil, errors.New("invalid_node")
	}
	raw, err := decode64(strings.TrimPrefix(link, "singbox://"))
	if err != nil || len(raw) > 128<<10 {
		return nil, errors.New("invalid_native_node")
	}
	var root Object
	if json.Unmarshal(raw, &root) != nil {
		return nil, errors.New("invalid_native_node")
	}
	var list []Object
	_, hasOutbounds := root["outbounds"]
	_, hasEndpoints := root["endpoints"]
	if hasOutbounds || hasEndpoints {
		if len(root) > 3 {
			return nil, errors.New("unsupported_native_section")
		}
		for k := range root {
			if k != "outbounds" && k != "endpoints" && k != "tag" {
				return nil, errors.New("unsupported_native_section")
			}
		}
		for _, section := range []string{"outbounds", "endpoints"} {
			entries, exists := root[section]
			if !exists {
				continue
			}
			values, ok := entries.([]any)
			if !ok || len(values) == 0 || len(values) > 16 {
				return nil, errors.New("invalid_native_node")
			}
			for _, v := range values {
				o, ok := v.(map[string]any)
				if !ok || section == "endpoints" && o["type"] != "wireguard" {
					return nil, errors.New("invalid_native_node")
				}
				list = append(list, o)
			}
		}
		if len(list) > 16 {
			return nil, errors.New("invalid_native_node")
		}
	} else {
		list = []Object{root}
	}
	names := map[string]string{}
	original := map[string]Object{}
	for i, o := range list {
		// The native JSON decoder matches field names without case. An alias
		// must not escape the closed-detour namespace enforced below.
		for key := range o {
			if strings.EqualFold(key, "detour") && key != "detour" {
				return nil, errors.New("unsupported_native_field")
			}
		}
		for _, key := range []string{"inbounds", "outbounds", "route", "dns", "log", "experimental", "services", "endpoints"} {
			if _, exists := o[key]; exists {
				return nil, errors.New("unsupported_native_section")
			}
		}
		kind, _ := o["type"].(string)
		if !nativeProxyTypes[kind] {
			return nil, errors.New("unsupported_protocol")
		}
		extra, err := validateExtraNode(o, kind)
		if err != nil {
			return nil, err
		}
		host, ok := o["server"].(string)
		port, pok := o["server_port"].(float64)
		if !extra && (!ok || host == "" || strings.ContainsAny(host, " /\\\x00\r\n\t@") || !pok || port < 1 || port > 65535 || port != float64(int(port))) {
			return nil, errors.New("invalid_native_node")
		}
		name, _ := o["tag"].(string)
		if name == "" {
			name = "out-" + strconv.Itoa(i)
		}
		if original[name] != nil {
			return nil, errors.New("invalid_native_node")
		}
		names[name] = tag + "/out-" + strconv.Itoa(i)
		original[name] = o
		if err := checkNativeFields(o); err != nil {
			return nil, err
		}
		if kind == "naive" {
			if _, exists := o["stream_receive_window"]; !exists {
				o["stream_receive_window"] = "8MB"
			}
		}
	}
	main, _ := root["tag"].(string)
	if len(list) == 1 {
		main, _ = list[0]["tag"].(string)
	}
	if main == "" {
		main, _ = list[0]["tag"].(string)
		if main == "" {
			main = "out-0"
		}
	}
	selected := original[main]
	if selected == nil {
		return nil, errors.New("invalid_native_node")
	}
	if selected["type"] == "shadowtls" {
		return nil, errors.New("shadowtls_requires_inner")
	}
	// All detours must terminate within this bundle, without cycles.
	for start := range original {
		seen := map[string]bool{}
		name := start
		for name != "" {
			if seen[name] || original[name] == nil {
				return nil, errors.New("invalid_native_detour")
			}
			seen[name] = true
			next, ok := original[name]["detour"]
			if !ok {
				break
			}
			name, ok = next.(string)
			if !ok || name == "" {
				return nil, errors.New("invalid_native_detour")
			}
		}
	}
	reachable := map[string]bool{}
	for name := main; name != ""; {
		reachable[name] = true
		next, _ := original[name]["detour"].(string)
		name = next
	}
	names[main] = tag
	var children []Object
	for name, o := range original {
		if !reachable[name] {
			continue
		}
		if next, ok := o["detour"].(string); ok {
			o["detour"] = names[next]
		}
		o["tag"] = names[name]
		if o["type"] == "tor" {
			o["_nailink_tor_identity"] = extraSourceIdentity(raw)
		}
		if name != main {
			children = append(children, o)
		}
	}
	selected["_nailink_children"] = children
	sort.Slice(children, func(i, j int) bool { return children[i]["tag"].(string) < children[j]["tag"].(string) })
	return selected, nil
}
func checkNativeFields(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, entry := range v {
			if strings.HasSuffix(strings.ToLower(key), "_path") && strings.ToLower(key) != "tcp_multi_path" {
				return errors.New("unsupported_native_field")
			}
			switch strings.ToLower(key) {
			case "certificate_path", "client_certificate_path", "client_key_path", "private_key_path", "executable_path", "data_directory", "command", "environment", "routing_mark", "network_namespace", "bind_interface", "bind_address", "domain_resolver":
				return errors.New("unsupported_native_field")
			}
			if strings.HasPrefix(strings.ToLower(key), "_nailink") {
				return errors.New("unsupported_native_field")
			}
			if strings.ToLower(key) == "plugin" {
				plugin, ok := entry.(string)
				if !ok || plugin != "obfs-local" && plugin != "v2ray-plugin" {
					return errors.New("unsupported_ss_plugin")
				}
			}
			if err := checkNativeFields(entry); err != nil {
				return err
			}
		}
	case []any:
		for _, entry := range v {
			if err := checkNativeFields(entry); err != nil {
				return err
			}
		}
	}
	return nil
}
func ExpandNode(node Object) []Object {
	children, _ := node["_nailink_children"].([]Object)
	delete(node, "_nailink_children")
	return append([]Object{node}, children...)
}
func NativeNodeMetadata(link string) (name, address, protocol string, err error) {
	node, err := NativeNode(link, "metadata")
	if err != nil {
		return "", "", "", err
	}
	protocol = node["type"].(string)
	if special, kind, ok := nativeExtraMetadata(node); ok {
		address, protocol = special, kind
	} else {
		address = net.JoinHostPort(node["server"].(string), strconv.Itoa(int(node["server_port"].(float64))))
	}
	var original Object
	raw, _ := decode64(strings.TrimPrefix(link, "singbox://"))
	_ = json.Unmarshal(raw, &original)
	name, _ = original["tag"].(string)
	if name == "" || len(name) > 120 || strings.ContainsAny(name, "\r\n\x00") || strings.Contains(name, "://") {
		name = protocol + "节点"
	}
	return name, address, protocol, nil
}
func NativeLink(value string) (string, error) {
	if len(value) > 128<<10 {
		return "", errors.New("invalid_native_node")
	}
	link := "singbox://" + base64.RawURLEncoding.EncodeToString([]byte(value))
	if _, err := NativeNode(link, "validate"); err != nil {
		return "", err
	}
	return link, nil
}
