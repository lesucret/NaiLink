// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
)

type RouterGeodataTransaction struct{ Target, Previous string }
type routerGeodataReceipt struct {
	Target   string `json:"target"`
	Previous string `json:"previous"`
	State    string `json:"state"`
}

var routerGeodataRoot atomic.Value
var routerGeodataName = regexp.MustCompile(`^data-[0-9]+$`)

func routerWriteGeodataReceipt(root string, tx *RouterGeodataTransaction, state string) error {
	data, err := json.Marshal(routerGeodataReceipt{tx.Target, tx.Previous, state})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), filepath.Join(root, "activation.json")); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func routerPointGeodata(root, target string) error {
	current := filepath.Join(root, "current")
	if target == "" {
		if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		file, err := os.CreateTemp(root, ".current-*")
		if err != nil {
			return err
		}
		name := file.Name()
		file.Close()
		os.Remove(name)
		defer os.Remove(name)
		if err = os.Symlink(target, name); err != nil {
			return err
		}
		if err = os.Rename(name, current); err != nil {
			return err
		}
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// A crash between pointer activation and a ready control plane must not make
// an unconfirmed batch the default at the next process start.
func routerRecoverGeodata() error {
	root, _ := routerGeodataRoot.Load().(string)
	if root == "" {
		return nil
	}
	rootInfo, rootErr := os.Lstat(root)
	if os.IsNotExist(rootErr) {
		return nil
	}
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe geodata recovery directory")
	}
	name := filepath.Join(root, "activation.json")
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024 {
		return errors.New("unsafe geodata receipt")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	var receipt routerGeodataReceipt
	if json.Unmarshal(data, &receipt) != nil || !routerGeodataName.MatchString(receipt.Target) || (receipt.Previous != "" && !routerGeodataName.MatchString(receipt.Previous)) {
		return errors.New("invalid geodata receipt")
	}
	if receipt.State == "applied" || receipt.State == "rolled_back" {
		return nil
	}
	if receipt.State != "applying" {
		return errors.New("invalid geodata receipt state")
	}
	current, err := os.Readlink(filepath.Join(root, "current"))
	if os.IsNotExist(err) {
		current = ""
	} else if err != nil {
		return err
	}
	if current != receipt.Target && current != receipt.Previous {
		return errors.New("geodata recovery pointer changed")
	}
	if receipt.Previous != "" {
		info, err = os.Lstat(filepath.Join(root, receipt.Previous))
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe previous geodata batch")
		}
		for _, asset := range []string{"geoip.dat", "geosite.dat"} {
			info, err = os.Lstat(filepath.Join(root, receipt.Previous, asset))
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("previous geodata unavailable")
			}
		}
	}
	if err = routerPointGeodata(root, receipt.Previous); err != nil {
		return err
	}
	return routerWriteGeodataReceipt(root, &RouterGeodataTransaction{receipt.Target, receipt.Previous}, "rolled_back")
}

func routerCommitGeodata(tx *RouterGeodataTransaction) error {
	root, _ := routerGeodataRoot.Load().(string)
	if tx == nil || root == "" {
		return errors.New("invalid geodata commit")
	}
	current, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil || current != tx.Target {
		return errors.New("geodata commit pointer changed")
	}
	return routerWriteGeodataReceipt(root, tx, "applied")
}

func routerSetGeodataRoot(dirs []string) {
	root := ""
	if len(dirs) > 0 {
		if absolute, err := filepath.Abs(dirs[0]); err == nil {
			root = filepath.Join(absolute, "geodata")
		}
	}
	routerGeodataRoot.Store(root)
}

// Called only by the serialized control-plane reload loop. Roll back the
// files BEFORE the existing control-plane fallback tries to read old rules.
func routerBeginGeodata(tx *RouterGeodataTransaction) (func() error, error) {
	root, _ := routerGeodataRoot.Load().(string)
	if root == "" || tx == nil || !routerGeodataName.MatchString(tx.Target) || (tx.Previous != "" && !routerGeodataName.MatchString(tx.Previous)) {
		return nil, errors.New("invalid geodata transaction")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe geodata directory")
	}
	for _, name := range []string{tx.Target, tx.Previous} {
		if name == "" {
			continue
		}
		directory := filepath.Join(root, name)
		info, err = os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("unsafe geodata batch")
		}
		for _, asset := range []string{"geoip.dat", "geosite.dat"} {
			info, err = os.Lstat(filepath.Join(directory, asset))
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64<<20 {
				return nil, errors.New("incomplete geodata batch")
			}
		}
	}
	current := filepath.Join(root, "current")
	previous, err := os.Readlink(current)
	if os.IsNotExist(err) {
		previous = ""
	} else if err != nil {
		return nil, errors.New("unsafe geodata pointer")
	}
	if previous != tx.Previous {
		return nil, errors.New("stale geodata transaction")
	}
	if err = routerWriteGeodataReceipt(root, tx, "applying"); err != nil {
		return nil, err
	}
	if err = routerPointGeodata(root, tx.Target); err != nil {
		if routerPointGeodata(root, tx.Previous) == nil {
			_ = routerWriteGeodataReceipt(root, tx, "rolled_back")
		}
		return nil, err
	}
	rollback := func() error {
		value, err := os.Readlink(current)
		if err != nil {
			return err
		}
		if value == tx.Previous {
			return routerWriteGeodataReceipt(root, tx, "rolled_back")
		}
		if value != tx.Target {
			return errors.New("geodata changed during rollback")
		}
		if err = routerPointGeodata(root, tx.Previous); err != nil {
			return err
		}
		return routerWriteGeodataReceipt(root, tx, "rolled_back")
	}
	return rollback, nil
}

func ActivateRouterGeodata(ctx context.Context, target, previous string) error {
	if !routerGeodataName.MatchString(target) || (previous != "" && !routerGeodataName.MatchString(previous)) {
		return errors.New("invalid geodata batch")
	}
	callback := make(chan error, 1)
	message := &ReloadMessage{Callback: callback, Geodata: &RouterGeodataTransaction{target, previous}}
	select {
	case ChReloadConfigs <- message:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-callback:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
