// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRouterGeodataPairSwitchRollbackAndStaleProtection(t *testing.T) {
	config := t.TempDir()
	root := filepath.Join(config, "geodata")
	os.Mkdir(root, 0700)
	routerSetGeodataRoot([]string{config})
	for _, name := range []string{"data-1", "data-2"} {
		os.Mkdir(filepath.Join(root, name), 0700)
		for _, asset := range []string{"geoip.dat", "geosite.dat"} {
			os.WriteFile(filepath.Join(root, name, asset), []byte(name+asset), 0600)
		}
	}
	os.Symlink("data-1", filepath.Join(root, "current"))
	rollback, err := routerBeginGeodata(&RouterGeodataTransaction{"data-2", "data-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"geoip.dat", "geosite.dat"} {
		data, _ := os.ReadFile(filepath.Join(root, "current", asset))
		if string(data) != "data-2"+asset {
			t.Fatal("pair not switched together")
		}
	}
	if err = rollback(); err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"geoip.dat", "geosite.dat"} {
		data, _ := os.ReadFile(filepath.Join(root, "current", asset))
		if string(data) != "data-1"+asset {
			t.Fatal("old pair unavailable for control-plane fallback")
		}
	}
	if _, err = routerBeginGeodata(&RouterGeodataTransaction{"data-2", "data-9"}); err == nil {
		t.Fatal("stale previous batch accepted")
	}
	for _, name := range []string{"../../outside", "data-2/../data-1", ""} {
		if _, err = routerBeginGeodata(&RouterGeodataTransaction{name, "data-1"}); err == nil {
			t.Fatal("unsafe batch accepted")
		}
	}
	os.Remove(filepath.Join(root, "data-2", "geoip.dat"))
	os.Symlink(filepath.Join(root, "data-1", "geoip.dat"), filepath.Join(root, "data-2", "geoip.dat"))
	if _, err = routerBeginGeodata(&RouterGeodataTransaction{"data-2", "data-1"}); err == nil {
		t.Fatal("symlinked data accepted")
	}
}

func TestRouterGeodataInitialRollbackAndCancellation(t *testing.T) {
	config := t.TempDir()
	root := filepath.Join(config, "geodata")
	os.Mkdir(root, 0700)
	os.Mkdir(filepath.Join(root, "data-1"), 0700)
	for _, asset := range []string{"geoip.dat", "geosite.dat"} {
		os.WriteFile(filepath.Join(root, "data-1", asset), []byte("fixture"), 0600)
	}
	routerSetGeodataRoot([]string{config})
	rollback, err := routerBeginGeodata(&RouterGeodataTransaction{"data-1", ""})
	if err != nil {
		t.Fatal(err)
	}
	if err = rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatal("initial rollback did not restore system-data search")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = ActivateRouterGeodata(ctx, "data-1", ""); err == nil {
		t.Fatal("cancellation lost")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	received := make(chan *ReloadMessage, 1)
	go func() { received <- <-ChReloadConfigs }()
	completed := make(chan error, 1)
	go func() { completed <- ActivateRouterGeodata(ctx, "data-1", "") }()
	message := <-received
	if message.Config != nil || message.Geodata == nil {
		t.Fatal("must reuse active configuration without a database apply")
	}
	cancel()
	if err = <-completed; err == nil {
		t.Fatal("callback wait ignored cancellation")
	}
	// Completion after the HTTP caller leaves must not block the reload loop.
	select {
	case message.Callback <- nil:
	case <-time.After(time.Second):
		t.Fatal("late callback blocked")
	}
}

func TestRouterGeodataCrashRecoveryAndCommittedBatch(t *testing.T) {
	for _, previous := range []string{"", "data-1"} {
		t.Run("previous-"+previous, func(t *testing.T) {
			config := t.TempDir()
			root := filepath.Join(config, "geodata")
			os.Mkdir(root, 0700)
			routerSetGeodataRoot([]string{config})
			for _, batch := range []string{"data-1", "data-2"} {
				os.Mkdir(filepath.Join(root, batch), 0700)
				for _, asset := range []string{"geoip.dat", "geosite.dat"} {
					os.WriteFile(filepath.Join(root, batch, asset), []byte(batch), 0600)
				}
			}
			if previous != "" {
				os.Symlink(previous, filepath.Join(root, "current"))
			}
			tx := &RouterGeodataTransaction{"data-2", previous}
			if _, err := routerBeginGeodata(tx); err != nil {
				t.Fatal(err)
			}
			// Model a process exit before readiness/commit, then the next Run.
			if err := routerRecoverGeodata(); err != nil {
				t.Fatal(err)
			}
			current, _ := os.Readlink(filepath.Join(root, "current"))
			if current != previous {
				t.Fatal("unconfirmed batch survived restart")
			}
			if _, err := routerBeginGeodata(tx); err != nil {
				t.Fatal(err)
			}
			if err := routerCommitGeodata(tx); err != nil {
				t.Fatal(err)
			}
			if err := routerRecoverGeodata(); err != nil {
				t.Fatal(err)
			}
			current, _ = os.Readlink(filepath.Join(root, "current"))
			if current != "data-2" {
				t.Fatal("committed batch was reverted")
			}
		})
	}
}
