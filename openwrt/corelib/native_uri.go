// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func IsNativeURI(link string) bool {
	for _, p := range []string{"singbox://", "hysteria://", "ssh://", "snell://"} {
		if strings.HasPrefix(strings.TrimSpace(link), p) {
			return true
		}
	}
	return false
}
func EncodeNativeURI(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Hostname() == "" {
		return "", errors.New("invalid_node")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("unsupported_node_path")
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid_node")
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return "", errors.New("invalid_node")
	}
	keys := ""
	switch u.Scheme {
	case "hysteria":
		keys = "auth peer sni insecure allowInsecure upmbps downmbps obfs protocol"
	case "ssh":
		keys = "host_key private_key private_key_passphrase client_version"
	case "snell":
		keys = "version obfs obfs-host mode"
	default:
		return "", errors.New("unsupported_protocol")
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields(keys) {
		allowed[k] = true
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return "", errors.New("unsupported_node_option")
		}
	}
	node := Object{"type": u.Scheme, "server": u.Hostname(), "server_port": port, "tag": u.Fragment}
	user, pass := "", ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	switch u.Scheme {
	case "hysteria":
		auth := user
		if pass != "" {
			auth += ":" + pass
		}
		if q.Get("auth") != "" {
			if auth != "" && auth != q.Get("auth") {
				return "", errors.New("conflicting_node_options")
			}
			auth = q.Get("auth")
		}
		node["auth_str"] = auth
		server := q.Get("peer")
		if q.Get("sni") != "" {
			if server != "" && server != q.Get("sni") {
				return "", errors.New("conflicting_node_options")
			}
			server = q.Get("sni")
		}
		tls := Object{"enabled": true}
		if server != "" {
			tls["server_name"] = server
		}
		for _, k := range []string{"insecure", "allowInsecure"} {
			if v := q.Get(k); v != "" {
				if v != "0" && v != "1" && v != "true" && v != "false" {
					return "", errors.New("invalid_tls_option")
				}
				b := v == "1" || v == "true"
				if prev, ok := tls["insecure"]; ok && prev != b {
					return "", errors.New("conflicting_node_options")
				}
				tls["insecure"] = b
			}
		}
		node["tls"] = tls
		for k, target := range map[string]string{"upmbps": "up_mbps", "downmbps": "down_mbps"} {
			if v := q.Get(k); v != "" {
				n, e := strconv.Atoi(v)
				if e != nil || n < 1 {
					return "", errors.New("invalid_bandwidth")
				}
				node[target] = n
			}
		}
		if q.Get("obfs") != "" {
			node["obfs"] = q.Get("obfs")
		}
		if v := q.Get("protocol"); v != "" && v != "udp" {
			return "", errors.New("unsupported_protocol")
		}
	case "ssh":
		node["user"], node["password"] = user, pass
		for _, k := range []string{"host_key", "private_key"} {
			if q.Get(k) != "" {
				node[k] = []string{q.Get(k)}
			}
		}
		for _, k := range []string{"private_key_passphrase", "client_version"} {
			if q.Get(k) != "" {
				node[k] = q.Get(k)
			}
		}
	case "snell":
		version := 4
		if q.Get("version") != "" {
			version, e = strconv.Atoi(q.Get("version"))
			if e != nil || version != 4 && version != 6 {
				return "", errors.New("unsupported_protocol")
			}
		}
		node["version"], node["psk"] = version, user
		if pass != "" {
			return "", errors.New("invalid_node")
		}
		for k, target := range map[string]string{"obfs": "obfs_mode", "obfs-host": "obfs_host", "mode": "mode"} {
			if q.Get(k) != "" {
				node[target] = q.Get(k)
			}
		}
	}
	raw, _ := json.Marshal(node)
	return NativeLink(string(raw))
}
