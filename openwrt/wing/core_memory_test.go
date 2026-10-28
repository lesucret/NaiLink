// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/control"
	"github.com/sirupsen/logrus"
)

func TestRouterMemoryUsesResidentPagesAndReportsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		text  string
		value uint64
		valid bool
	}{
		{"VmSize: 1524676 kB\nVmRSS: 144688 kB\nRssAnon: 94704 kB\n", 144688, true},
		{"VmRSS: 0 kB\n", 0, true},
		{"VmSize: 1524676 kB\n", 0, false},
		{"VmRSS: -1 kB\n", 0, false},
		{"VmRSS: 100 MB\n", 0, false},
	} {
		if value, valid := routerParseResidentKiB([]byte(tc.text)); value != tc.value || valid != tc.valid {
			t.Fatal(tc.text, value, valid)
		}
	}
	backend, ok := routerResidentKiB(os.Getpid())
	if !ok || backend == 0 {
		t.Fatal("cannot read actual resident memory")
	}
	native := &routerSingBoxSession{cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}, exit: make(chan struct{})}
	if memory := routerCoreResidentMemory(native, "mihomo"); !memory.Complete || memory.NativeKiB == nil || memory.TotalKiB != memory.BackendKiB+*memory.NativeKiB {
		t.Fatal(memory)
	}
	native.hasTor = true
	if routerCoreResidentMemory(native, "sing-box").Complete {
		t.Fatal("omitted Tor memory from total without marking it partial")
	}
	close(native.exit)
	if memory := routerCoreResidentMemory(native, "mihomo"); memory.Complete || memory.NativeKiB != nil {
		t.Fatal("counted a stopped process", memory)
	}
	if memory := routerCoreResidentMemory(nil, "dae"); !memory.Complete || memory.NativeKiB != nil {
		t.Fatal(memory)
	}
}

func TestRouterCoreAllDirectionsAndReadinessFailureRollback(t *testing.T) {
	for _, pair := range [][2]string{{"dae", "sing-box"}, {"dae", "mihomo"}, {"sing-box", "dae"}, {"sing-box", "mihomo"}, {"mihomo", "dae"}, {"mihomo", "sing-box"}} {
		for _, fail := range []bool{false, true} {
			t.Run(pair[0]+"-"+pair[1]+"-"+strconv.FormatBool(fail), func(t *testing.T) {
				conf, dirs := coreFixtureConfig(t)
				key := restartConfigKey(conf)
				liveCount := 1
				log := logrus.New()
				log.SetOutput(io.Discard)
				old := &coreFakeSession{live: &liveCount, done: make(chan error)}
				m := &routerCoreManager{root: dirs[0], dirs: dirs, conf: conf, log: log, state: routercore.State{Current: pair[0], Phase: "stable"}, phase: "ready", active: old}
				m.factory = func(_ context.Context, name string, config *daeConfig.Config) (routerCoreSession, error) {
					if liveCount != 0 || restartConfigKey(config) != key {
						t.Fatal("overlapping cores or changed configuration")
					}
					liveCount++
					next := &coreFakeSession{live: &liveCount, done: make(chan error)}
					if fail && name == pair[1] {
						next.readyErr = errors.New("core_dns_failed")
					}
					return next, nil
				}
				err := m.switchPrepared(context.Background(), pair[1])
				want := pair[1]
				if fail {
					want = pair[0]
					if err == nil || err.Error() != "core_rolled_back" {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if m.state.Current != want || m.phase != "ready" || liveCount != 1 || restartConfigKey(m.conf) != key {
					t.Fatal(m.state, liveCount)
				}
			})
		}
	}
}

func TestRouterRetiredDaeDoesNotRetainServingReference(t *testing.T) {
	t.Cleanup(func() { currentControlPlane.Store(nil); routerServingPlane = nil; routerConfigured = false })
	old, next := new(control.ControlPlane), new(control.ControlPlane)
	routerPublishControlPlane(old, &daeConfig.Config{})
	routerMarkServing(old)
	if err := routerCloseControlPlane(old); err != nil {
		t.Fatal(err)
	}
	if currentControlPlane.Load() != nil || routerServingPlane != nil || routerConfigured {
		t.Fatal("retained a closed dae generation")
	}
	if status := readRouterDaeReadiness(false); status.Ready || status.Listening {
		t.Fatal("retired plane still appears ready", status)
	}
	routerPublishControlPlane(next, &daeConfig.Config{})
	routerMarkServing(next)
	if err := routerCloseControlPlane(old); err != nil {
		t.Fatal(err)
	}
	if currentControlPlane.Load() != next || routerServingPlane != next || !routerConfigured {
		t.Fatal("closing an old generation unpublished its successor")
	}
}

func TestRouterReloadReleasesRetiredNativeWrapperAndPreservesRollbackConfig(t *testing.T) {
	for _, failure := range []error{nil, errors.New("core_reload_rolled_back")} {
		previous, candidate := &daeConfig.Config{}, &daeConfig.Config{}
		live := &routerSingBoxSession{conf: previous}
		retired := &routerSingBoxSession{replacement: live, dnsBridge: &mhDNSBridge{upstreams: map[string]sbObject{"old": {}}}}
		m := &routerCoreManager{active: retired, conf: previous}
		m.finishReload(retired, &ReloadMessage{Config: candidate}, failure)
		if m.active != live {
			t.Fatal("retained stopped wrapper and old matchers")
		}
		if failure == nil && m.conf != candidate || failure != nil && m.conf != previous {
			t.Fatal("lost running configuration after reload/rollback")
		}
		m.finishReload(retired, &ReloadMessage{Config: previous}, nil)
		if m.active != live || failure == nil && m.conf != candidate {
			t.Fatal("stale completion replaced newer session")
		}
	}
}
