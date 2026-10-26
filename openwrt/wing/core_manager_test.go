// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

type coreFakeSession struct {
	live              *int
	readyErr, stopErr error
	done              chan error
	stopped           bool
}

func TestRouterCoreRandomRedirectTableOwnership(t *testing.T) {
	_, loop, _ := net.ParseCIDR("127.0.0.1/32")
	owner := routerCoreNetworkOwner{BaselineTables: []int{254, 255, 70000}}
	routes := []netlink.Route{{Table: 70000, Type: 2, Dst: loop}, {Table: 80000, Type: 2, Dst: loop}}
	table, err := routerRedirectTable(owner, routes)
	if err != nil || table != 80000 {
		t.Fatal(table, err)
	}
	owner.RedirectTable = 80000
	if table, err = routerRedirectTable(owner, nil); err != nil || table != 80000 {
		t.Fatal("lost recorded ownership after native cleanup removed routes", table, err)
	}
	_, foreign, _ := net.ParseCIDR("10.0.0.0/8")
	if _, err = routerRedirectTable(owner, []netlink.Route{{Table: 80000, Type: 1, Dst: foreign}}); err == nil {
		t.Fatal("accepted foreign routes in recorded table")
	}
}

func TestRouterCoreOldBootOwnerNeverTouchesReusedPID(t *testing.T) {
	root := t.TempDir()
	oldInclude := routerCoreFW4Include
	routerCoreFW4Include = filepath.Join(root, "missing-include")
	defer func() { routerCoreFW4Include = oldInclude }()
	owner := routerCoreNetworkOwner{Config: filepath.Join(routerCoreRuntimeDir, "active-fixture", "config.json"), PID: os.Getpid(), Started: routerProcessStart(os.Getpid()), BootID: "previous-boot", RedirectTable: 999999}
	data, _ := json.Marshal(owner)
	if err := routercore.Write(routerOwnerPath(root), data); err != nil {
		t.Fatal(err)
	}
	if err := routerRecoverSingboxNetwork(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(routerOwnerPath(root)); !os.IsNotExist(err) {
		t.Fatal("old boot claim retained", err)
	}
}

func TestRouterCoreExitedSessionRecoversWithoutEditingConfig(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	key := restartConfigKey(conf)
	log := logrus.New()
	log.SetOutput(io.Discard)
	m := &routerCoreManager{root: dirs[0], conf: conf, log: log, state: routercore.State{Current: "sing-box", Phase: "stable"}}
	live := 0
	m.factory = func(context.Context, string, *daeConfig.Config) (routerCoreSession, error) {
		live++
		return &coreFakeSession{live: &live, done: make(chan error)}, nil
	}
	if err := m.recoverExited(); err != nil {
		t.Fatal(err)
	}
	if m.state.Code != "core_restarted" || m.state.Current != "sing-box" || live != 1 || key != restartConfigKey(conf) {
		t.Fatal(m.state)
	}
}

func (s *coreFakeSession) Reload(*ReloadMessage) error { return nil }
func (s *coreFakeSession) Ready(context.Context) error { return s.readyErr }
func (s *coreFakeSession) Done() <-chan error          { return s.done }
func (s *coreFakeSession) Stop(context.Context) error {
	if s.stopErr != nil {
		return s.stopErr
	}
	if !s.stopped {
		*s.live--
		s.stopped = true
	}
	return nil
}
func TestRouterCoreTransactionalSwitch(t *testing.T) {
	for _, scenario := range []string{"success", "not_ready", "rollback_fails", "cleanup_fails", "old_stop_fails"} {
		t.Run(scenario, func(t *testing.T) {
			conf, dirs := coreFixtureConfig(t)
			original := restartConfigKey(conf)
			root := dirs[0]
			database := filepath.Join(root, "wing.db")
			if err := os.WriteFile(database, []byte("node-and-config-sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			live, starts := 1, 0
			old := &coreFakeSession{live: &live, done: make(chan error)}
			if scenario == "old_stop_fails" {
				old.stopErr = errors.New("refuse stop")
			}
			log := logrus.New()
			log.SetOutput(io.Discard)
			m := &routerCoreManager{root: root, conf: conf, dirs: dirs, log: log, state: routercore.State{Current: "dae", Phase: "stable"}, phase: "ready", active: old}
			m.factory = func(_ context.Context, name string, _ *daeConfig.Config) (routerCoreSession, error) {
				starts++
				if live != 0 {
					t.Fatal("two cores took network ownership")
				}
				s := &coreFakeSession{live: &live, done: make(chan error)}
				live++
				if name == "sing-box" && scenario != "success" {
					s.readyErr = errors.New("startup failed")
					if scenario == "cleanup_fails" {
						s.stopErr = errors.New("cleanup failed")
					}
				}
				if name == "dae" && scenario == "rollback_fails" {
					s.readyErr = errors.New("old core also fails")
				}
				return s, nil
			}
			err := m.switchPrepared(context.Background(), "sing-box")
			switch scenario {
			case "success":
				if err != nil || m.state.Current != "sing-box" || live != 1 {
					t.Fatal(m.state, live, err)
				}
				if err = m.switchPrepared(context.Background(), "dae"); err != nil || m.state.Current != "dae" || live != 1 {
					t.Fatal(m.state, err)
				}
			case "not_ready":
				if err == nil || err.Error() != "core_rolled_back" || m.state.Current != "dae" || m.state.Phase != "rolled_back" || live != 1 {
					t.Fatal(m.state, live, err)
				}
			case "rollback_fails":
				if err == nil || err.Error() != "core_rollback_failed" || m.state.Phase != "rollback_failed" || live != 0 {
					t.Fatal(m.state, live, err)
				}
			case "cleanup_fails":
				if err == nil || err.Error() != "core_cleanup_failed" || starts != 1 {
					t.Fatal("started old core despite uncertain cleanup", starts, err)
				}
			case "old_stop_fails":
				if err == nil || starts != 0 {
					t.Fatal("started new core despite a live previous core")
				}
			}
			if original != restartConfigKey(conf) {
				t.Fatal("changed current configuration")
			}
			if data, _ := os.ReadFile(database); string(data) != "node-and-config-sentinel" {
				t.Fatal("modified database")
			}
			if data, err := routercore.Read(filepath.Join(routercore.Root(root), "previous.dae"), 16<<20); err != nil || len(data) == 0 {
				t.Fatal("lost recovery snapshot", err)
			}
		})
	}
}
func TestRouterCoreInterruptedSwitchRestoresPreviousSelection(t *testing.T) {
	conf, dirs := coreFixtureConfig(t)
	root := dirs[0]
	snapshot, err := conf.Marshal(2)
	if err != nil {
		t.Fatal(err)
	}
	if err = routercore.Write(filepath.Join(routercore.Root(root), "previous.dae"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err = routercore.SaveState(root, routercore.State{Current: "dae", Previous: "dae", Target: "sing-box", Phase: "switching", Running: true}); err != nil {
		t.Fatal(err)
	}
	if err = InitializeRouterCores(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		routerCoreGlobal.Lock()
		defer routerCoreGlobal.Unlock()
		if routerCoreGlobal.lock != nil {
			_ = routerCoreGlobal.lock.Close()
		}
		routerCoreGlobal.lock = nil
		routerCoreGlobal.root = ""
		routerCoreGlobal.recovered = nil
		routerCoreGlobal.manager = nil
	})
	if !RouterCoreRecovered() {
		t.Fatal("startup lost incomplete transaction")
	}
	state, err := routercore.ReadState(root)
	if err != nil || state.Current != "dae" || state.Target != "sing-box" {
		t.Fatal("committed target before a ready core", state, err)
	}
	if _, err = routerCoreManagerLock(root); err == nil {
		t.Fatal("allowed two managers for one configuration")
	}
}
