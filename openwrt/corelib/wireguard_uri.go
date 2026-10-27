// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func wireguardURI(u *url.URL) (string, error) {
	if u.User == nil || u.Hostname() == "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("invalid_node")
	}
	port := 51820
	var err error
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return "", errors.New("invalid_wireguard_peer")
		}
	}
	private := u.User.Username()
	if _, ok := u.User.Password(); ok {
		return "", errors.New("invalid_wireguard_key")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("invalid_node")
	}
	allowed := map[string]bool{"publickey": true, "public_key": true, "address": true, "mtu": true, "reserved": true, "presharedkey": true, "pre_shared_key": true, "keepalive": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return "", OptionProblem("unsupported_node_option", key)
		}
	}
	public := q.Get("publickey")
	if v := q.Get("public_key"); v != "" {
		if public != "" && public != v {
			return "", errors.New("conflicting_node_options")
		}
		public = v
	}
	peer := Object{"address": u.Hostname(), "port": port, "public_key": public, "allowed_ips": []string{"0.0.0.0/0", "::/0"}}
	psk := q.Get("presharedkey")
	if v := q.Get("pre_shared_key"); v != "" {
		if psk != "" && psk != v {
			return "", errors.New("conflicting_node_options")
		}
		psk = v
	}
	if psk != "" {
		peer["pre_shared_key"] = psk
	}
	if value := q.Get("reserved"); value != "" {
		var reserved []int
		for _, v := range strings.Split(value, ",") {
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 || n > 255 {
				return "", errors.New("invalid_wireguard_peer")
			}
			reserved = append(reserved, n)
		}
		if len(reserved) != 3 {
			return "", errors.New("invalid_wireguard_peer")
		}
		peer["reserved"] = reserved
	}
	if value := q.Get("keepalive"); value != "" {
		n, e := strconv.Atoi(value)
		if e != nil || n < 0 || n > 65535 {
			return "", errors.New("invalid_wireguard_peer")
		}
		peer["persistent_keepalive_interval"] = n
	}
	node := Object{"type": "wireguard", "tag": u.Fragment, "system": false, "private_key": private, "address": strings.Split(q.Get("address"), ","), "peers": []Object{peer}, "workers": 1}
	if value := q.Get("mtu"); value != "" {
		n, e := strconv.Atoi(value)
		if e != nil {
			return "", errors.New("invalid_wireguard_mtu")
		}
		node["mtu"] = n
	}
	raw, _ := json.Marshal(node)
	return NativeLink(string(raw))
}
