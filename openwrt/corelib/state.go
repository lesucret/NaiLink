// SPDX-License-Identifier: AGPL-3.0-only
// Package routercore contains the shared, database-independent core contract.
package routercore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const SingBoxVersion = "1.14.3"
const BinaryPath = "/usr/libexec/daed/cores/sing-box"
const TunName = "nailink0"

type Release struct{ CPU, Archive, SHA256 string }

var Releases = map[string]Release{
	"amd64": {"x86_64", "sing-box_1.14.3_openwrt_x86_64.ipk", "c7b2945a72cb4836084a9bc1f3930ec11094c7362fd179c93fcaf401bfac26fa"},
	"arm64": {"arm64", "sing-box_1.14.3_openwrt_aarch64_generic.ipk", "fb7d65d085c42a49b705bd38d639062a0c2f1cca2fb1c1dc2d41277ad840c4c6"},
}

type State struct {
	Operation string `json:"operation,omitempty"`
	Current   string `json:"current"`
	Previous  string `json:"previous,omitempty"`
	Target    string `json:"target,omitempty"`
	Phase     string `json:"phase"`
	Code      string `json:"code,omitempty"`
	Running   bool   `json:"running,omitempty"`
}
type Plan struct {
	Target     string   `json:"target"`
	Revision   string   `json:"revision"`
	Compatible bool     `json:"compatible"`
	Installed  bool     `json:"installed"`
	Warnings   []string `json:"warnings"`
	Problems   []string `json:"problems"`
	Nodes      int      `json:"nodes"`
	Groups     int      `json:"groups"`
}

func ValidCore(name string) bool { return name == "dae" || name == "sing-box" }
func Root(config string) string  { return filepath.Join(config, "cores") }

// Never follow substituted private state directories or files.
func PrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe_core_state")
	}
	return nil
}
func Write(path string, data []byte) error {
	if err := PrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".core-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func Read(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > limit {
		return nil, errors.New("unsafe_core_state")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("unsafe_core_state")
	}
	return data, err
}
func ReadState(config string) (State, error) {
	state := State{Current: "dae", Phase: "stable"}
	root := Root(config)
	if st, err := os.Lstat(root); err == nil && (!st.IsDir() || st.Mode().Perm()&0077 != 0) {
		return state, errors.New("unsafe_core_state")
	} else if err != nil && !os.IsNotExist(err) {
		return state, err
	}
	data, err := Read(filepath.Join(root, "state.json"), 16384)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if json.Unmarshal(data, &state) != nil || !ValidCore(state.Current) ||
		(state.Previous != "" && !ValidCore(state.Previous)) || (state.Target != "" && !ValidCore(state.Target)) {
		return state, errors.New("unsafe_core_state")
	}
	switch state.Phase {
	case "stable", "switching", "rolled_back", "rollback_failed", "failed":
	default:
		return state, errors.New("unsafe_core_state")
	}
	if state.Phase == "switching" && state.Previous == "" {
		return state, errors.New("unsafe_core_state")
	}
	return state, nil
}
func SaveState(config string, state State) error {
	if !ValidCore(state.Current) {
		return errors.New("invalid_core")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return Write(filepath.Join(Root(config), "state.json"), data)
}
func Fingerprint(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func ValidRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
