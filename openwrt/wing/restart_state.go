// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/control"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
)

const routerRestartLimit = 1024
const routerRestartBytes = 2 << 20
const routerRestartAge = 10 * time.Minute

// This lock covers diagnostics against generation Close, not proxy traffic.
var routerLifecycleMu sync.RWMutex
var routerRecoveryMu sync.Mutex
var routerRecoveryResult = "absent"
var routerRecoveryCount int
var routerConfigured bool
var routerServingPlane *control.ControlPlane

type restartDNSEntry struct {
	Key              string    `json:"key"`
	Owner            string    `json:"owner"`
	Message          []byte    `json:"message"`
	Deadline         time.Time `json:"deadline"`
	OriginalDeadline time.Time `json:"original_deadline"`
}

type restartState struct {
	Version int               `json:"version"`
	Config  string            `json:"config"`
	SavedAt time.Time         `json:"saved_at"`
	Entries []restartDNSEntry `json:"entries"`
}

func restartConfigKey(conf *daeConfig.Config) string {
	data, err := json.Marshal(conf)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func encodeRestartState(conf *daeConfig.Config, entries map[string]*control.DnsCache, now time.Time) ([]byte, error) {
	state := restartState{Version: 1, Config: restartConfigKey(conf), SavedAt: now}
	if state.Config == "" {
		return nil, errors.New("invalid restart configuration")
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	budget := 0
	for _, key := range keys {
		cache := entries[key]
		if len(key) == 0 || len(key) > 512 || cache == nil || len(cache.RouteOwnerKey) > 512 || !cache.Deadline.After(now) || !cache.OriginalDeadline.After(now) {
			continue
		}
		message := &dnsmessage.Msg{Answer: cache.Answer, Ns: cache.NS, Extra: cache.Extra}
		data, err := message.Pack()
		if err != nil || len(data) > 65535 {
			continue
		}
		// Base64 and JSON metadata must also fit inside the read limit.
		cost := len(data)*2 + len(key)*6 + len(cache.RouteOwnerKey)*6 + 512
		if budget+cost > routerRestartBytes-1024 {
			break
		}
		budget += cost
		state.Entries = append(state.Entries, restartDNSEntry{key, cache.RouteOwnerKey, data, cache.Deadline, cache.OriginalDeadline})
		if len(state.Entries) == routerRestartLimit {
			break
		}
	}
	return json.Marshal(state)
}

func decodeRestartState(data []byte, conf *daeConfig.Config, now time.Time) (map[string]*control.DnsCache, error) {
	if len(data) > routerRestartBytes {
		return nil, errors.New("restart snapshot too large")
	}
	var state restartState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, errors.New("invalid restart snapshot")
	}
	if state.Version != 1 || state.Config != restartConfigKey(conf) || len(state.Entries) > routerRestartLimit {
		return nil, errors.New("restart snapshot does not match configuration")
	}
	if state.SavedAt.After(now.Add(time.Second)) || now.Sub(state.SavedAt) > routerRestartAge {
		return nil, errors.New("restart snapshot expired")
	}
	result := make(map[string]*control.DnsCache)
	for _, entry := range state.Entries {
		if len(entry.Key) == 0 || len(entry.Key) > 512 || len(entry.Owner) > 512 || !entry.Deadline.After(now) || !entry.OriginalDeadline.After(now) {
			continue
		}
		if _, duplicate := result[entry.Key]; duplicate {
			return nil, errors.New("duplicate restart DNS entry")
		}
		message := new(dnsmessage.Msg)
		if len(entry.Message) > 65535 || message.Unpack(entry.Message) != nil || len(message.Answer) == 0 {
			continue
		}
		result[entry.Key] = &control.DnsCache{RouteOwnerKey: entry.Owner, Answer: message.Answer, NS: message.Ns, Extra: message.Extra,
			Deadline: entry.Deadline, OriginalDeadline: entry.OriginalDeadline}
	}
	return result, nil
}

func routerRestoreRestartDNS(log *logrus.Logger, conf *daeConfig.Config, existing map[string]*control.DnsCache) map[string]*control.DnsCache {
	if conf == EmptyConfig || len(existing) != 0 {
		return existing
	}
	path := os.Getenv("DAED_RESTART_STATE")
	if path == "" {
		return existing
	}
	routerRecoveryMu.Lock()
	defer routerRecoveryMu.Unlock()
	routerRecoveryCount = 0
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		routerRecoveryResult = "absent"
		return existing
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > routerRestartBytes {
		routerRecoveryResult = "rejected"
		return existing
	}
	file, err := os.Open(path)
	if err != nil {
		routerRecoveryResult = "unreadable"
		return existing
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, routerRestartBytes+1))
	if err == nil {
		existing, err = decodeRestartState(data, conf, time.Now())
	}
	if err != nil {
		routerRecoveryResult = "expired_or_mismatch"
		log.Info("Restart DNS snapshot skipped; current configuration and DNS TTLs take precedence")
		return nil
	}
	routerRecoveryResult, routerRecoveryCount = "restored", len(existing)
	log.WithField("entries", len(existing)).Info("Restoring restart DNS address knowledge with current routing rules")
	return existing
}

func routerSaveRestartDNS(log *logrus.Logger, plane *control.ControlPlane, conf *daeConfig.Config) {
	path := os.Getenv("DAED_RESTART_STATE")
	if path == "" || plane == nil || conf == EmptyConfig {
		return
	}
	now := time.Now()
	data, err := encodeRestartState(conf, plane.SnapshotRestartDNS(routerRestartLimit, now), now)
	if err != nil {
		log.Warn("Cannot encode restart DNS snapshot")
		return
	}
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0700); err != nil {
		log.Warn("Cannot create restart state directory")
		return
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		log.Warn("Restart state directory rejected")
		return
	}
	if err = os.Chmod(directory, 0700); err != nil {
		log.Warn("Cannot protect restart state directory")
		return
	}
	file, err := os.CreateTemp(directory, ".restart-*")
	if err != nil {
		log.Warn("Cannot create restart DNS snapshot")
		return
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		log.Warn("Cannot save restart DNS snapshot")
		return
	}
	log.Info("Saved bounded restart DNS snapshot in volatile storage")
}

func routerPublishControlPlane(plane *control.ControlPlane, conf *daeConfig.Config) {
	routerLifecycleMu.Lock()
	currentControlPlane.Store(plane)
	routerConfigured = conf != EmptyConfig
	routerServingPlane = nil
	routerLifecycleMu.Unlock()
}

func routerMarkServing(plane *control.ControlPlane) {
	routerLifecycleMu.Lock()
	defer routerLifecycleMu.Unlock()
	if currentControlPlane.Load() == plane {
		routerServingPlane = plane
	}
}

func routerCloseControlPlane(plane *control.ControlPlane) error {
	routerLifecycleMu.Lock()
	defer routerLifecycleMu.Unlock()
	return plane.Close()
}

type RouterReadiness struct {
	Core       string `json:"core"`
	Phase      string `json:"phase"`
	Ready      bool   `json:"ready"`
	Configured bool   `json:"configured"`
	Listening  bool   `json:"listening"`
	control.RouterDataplaneStatus
	RestartDNS  string `json:"restart_dns"`
	RestoredDNS int    `json:"restored_dns_entries"`
}

func readRouterDaeReadiness(details bool) RouterReadiness {
	result := RouterReadiness{Phase: "starting"}
	if !routerLifecycleMu.TryRLock() {
		return result
	}
	defer routerLifecycleMu.RUnlock()
	plane := currentControlPlane.Load()
	if plane == nil {
		return result
	}
	result.Configured = routerConfigured
	result.RouterDataplaneStatus = plane.RouterDataplaneStatus(details)
	result.Listening = routerServingPlane == plane
	routerRecoveryMu.Lock()
	result.RestartDNS, result.RestoredDNS = routerRecoveryResult, routerRecoveryCount
	routerRecoveryMu.Unlock()
	if !result.Configured {
		result.Phase = "inactive"
		result.Ready = true
	} else if !result.Listening || !result.Initialized {
		result.Phase = "dataplane_starting"
	} else if !result.RoutingReady {
		result.Phase = "dns_route_restore_failed"
	} else if !result.DNSReady {
		result.Phase = "dns_starting"
	} else {
		result.Phase = "ready"
		result.Ready = true
	}
	return result
}
