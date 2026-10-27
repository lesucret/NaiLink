// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"errors"
	"os"
	"path/filepath"
)

const ExtraVersion = "1.14.3-nailink.2"
const ExtraRelease = "openwrt-v0.6.39"
const ExtraRoot = "/usr/libexec/daed/components"
const TorVersion = "0.4.9.13"

var ExtraReleases = map[string]Release{
	"amd64": {"x86_64", "nailink-sing-box-extra-1.14.3-nailink.2-x86_64.tar.gz", "315233fbd665ec15c7fb5af0454d79a368d9dd3b9045da73c23d8d06cdf7809f"},
	"arm64": {"arm64", "nailink-sing-box-extra-1.14.3-nailink.2-arm64.tar.gz", "79c80a537dccb4bf9009a3851ab9af2bd34ef24619bd04a1cf2fd7228085cf76"},
}

// The active link can only resolve to a private direct child of our component
// directory. No imported node can supply an executable or library path.
func ExtraDirectory(root string) (string, error) {
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("extra_not_installed")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, "extra"))
	if err != nil || filepath.Dir(resolved) != root {
		return "", errors.New("extra_not_installed")
	}
	st, err = os.Lstat(resolved)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("extra_not_installed")
	}
	for _, name := range []string{"sing-box", "tor-launch", "tor/bin/tor"} {
		st, err = os.Lstat(filepath.Join(resolved, name))
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
			return "", errors.New("extra_not_installed")
		}
	}
	return resolved, nil
}

// Split userspace WireGuard endpoints from outbounds only after the common
// namespace validation. The selected tag remains unchanged for group routing.
func PrepareExtraProfile(profile Object, root, dataRoot, purpose string) (bool, bool, error) {
	outbounds, _ := profile["outbounds"].([]Object)
	var regular, endpoints []Object
	required, hasTor := false, false
	torCount := 0
	for _, node := range outbounds {
		kind := NativeFeature(node)
		if kind != "" {
			required = true
		}
		if kind == "wireguard" {
			endpoints = append(endpoints, node)
			continue
		}
		if kind == "tor" {
			torCount++
			hasTor = true
			identity, _ := node["_nailink_tor_identity"].(string)
			delete(node, "_nailink_tor_identity")
			if !ValidRevision(identity) || purpose != "main" && purpose != "probe" {
				return false, false, errors.New("unsupported_tor_option")
			}
			// Resolve only managed immutable paths. Check-only conversion may happen
			// before installation; report the missing bundle without changing data.
			directory, err := ExtraDirectory(root)
			if err != nil {
				return false, false, err
			}
			node["executable_path"] = filepath.Join(directory, "tor-launch")
			node["data_directory"] = filepath.Join(dataRoot, identity, purpose)
			values := map[string]any{}
			if old, ok := node["torrc"].(map[string]any); ok {
				for k, v := range old {
					values[k] = v
				}
			}
			values["ClientOnly"] = "1"
			values["AvoidDiskWrites"] = "1"
			values["MaxMemInQueues"] = "64 MB"
			values["GeoIPFile"] = filepath.Join(directory, "tor/share/geoip")
			values["GeoIPv6File"] = filepath.Join(directory, "tor/share/geoip6")
			node["torrc"] = values
		}
		regular = append(regular, node)
	}
	if torCount > 1 {
		return false, false, errors.New("too_many_tor_nodes")
	}
	profile["outbounds"] = regular
	if len(endpoints) > 0 {
		profile["endpoints"] = endpoints
	}
	return required, hasTor, nil
}
