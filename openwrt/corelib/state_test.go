// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDurabilityAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	state := State{Current: "dae", Previous: "dae", Target: "sing-box", Phase: "switching", Running: true}
	if err := SaveState(root, state); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(root)
	if err != nil || got != state {
		t.Fatal(got, err)
	}
	path := filepath.Join(Root(root), "state.json")
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadState(root); err == nil {
		t.Fatal("accepted public state")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "external"), path); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadState(root); err == nil {
		t.Fatal("followed substituted state")
	}
}
