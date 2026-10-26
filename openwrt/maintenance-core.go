// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/daeuniverse/dae-wing/routercore"
)

var coreMaintenanceCodes = []string{"core_incompatible", "core_configuration_changed", "singbox_not_installed", "singbox_version_mismatch", "singbox_configuration_invalid", "core_environment_unsupported", "core_network_conflict", "core_rolled_back", "core_rollback_failed", "core_cleanup_failed", "core_stop_failed", "core_state_failed", "core_snapshot_failed", "core_start_failed", "core_not_ready", "core_connectivity_failed", "core_dependency_failed", "core_exited", "core_not_running", "nftables_unavailable"}

func coreMaintenanceCode(message string) string {
	if message == "core_request_failed" {
		return message
	}
	for _, code := range []string{"core_dns_failed", "core_reload_rolled_back", "core_recovery_failed", "core_network_check_failed", "core_network_ownership_failed", "core_process_ownership_failed", "core_ports_unavailable", "invalid_core_request", "core_recovery_snapshot_missing", "core_recovery_snapshot_invalid"} {
		if message == code {
			return code
		}
	}
	for _, code := range coreMaintenanceCodes {
		if message == code {
			return code
		}
	}
	return ""
}

func uncertainCoreJob(job maintenanceJob) bool {
	if !strings.HasPrefix(job.Action, "core_switch_") {
		return false
	}
	return job.Code == "backend_unavailable" || job.Code == "core_request_failed" || job.Code == "interrupted"
}
func coreMaintenanceAction(action string) bool {
	switch action {
	case "core_check_dae", "core_check_singbox", "core_install_singbox", "core_switch_dae", "core_switch_singbox":
		return true
	}
	return false
}

// Match the durable transaction ID, never infer success from a selected core
// left by an unrelated operation after the worker/HTTP response was lost.
func (m *maintenanceManager) reconcileCoreJob(job *maintenanceJob) {
	state, err := routercore.ReadState(m.configDir)
	if err != nil || state.Operation != job.ID {
		return
	}
	if state.Phase == "switching" {
		job.State, job.Code, job.Phase = "running", "", "switching"
	} else if state.Phase == "stable" && state.Current == job.Target {
		job.State, job.Code = "success", "recovered"
	} else if state.Code != "" {
		job.Code = state.Code
	}
}

type coreMaintenanceStatus struct {
	Current   string           `json:"current"`
	Phase     string           `json:"phase"`
	Code      string           `json:"code,omitempty"`
	Installed bool             `json:"installed"`
	Supported bool             `json:"supported"`
	Version   string           `json:"version"`
	Plan      *routercore.Plan `json:"plan,omitempty"`
}
type storedCorePlan struct {
	Plan    routercore.Plan `json:"plan"`
	Checked int64           `json:"checked"`
}

func (m *maintenanceManager) corePath() string {
	if m.coreBinary != "" {
		return m.coreBinary
	}
	return routercore.BinaryPath
}
func (m *maintenanceManager) coreStatus() *coreMaintenanceStatus {
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return &coreMaintenanceStatus{Current: "unknown", Phase: "failed", Code: "unsafe_state"}
	}
	_, supported := routercore.Releases[runtime.GOARCH]
	status := &coreMaintenanceStatus{Current: state.Current, Phase: state.Phase, Code: state.Code, Supported: supported, Version: routercore.SingBoxVersion}
	if st, err := os.Lstat(m.corePath()); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 {
		status.Installed = true
	}
	var saved storedCorePlan
	if readMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), &saved) == nil && m.now().Unix()-saved.Checked <= 600 && saved.Checked <= m.now().Unix()+60 {
		status.Plan = &saved.Plan
	}
	return status
}
func (m *maintenanceManager) maintainCore(ctx context.Context, job *maintenanceJob) error {
	lock, err := maintenanceLock(m.installLock, false)
	if err != nil {
		return errors.New("busy")
	}
	defer lock.Close()
	if job.Action == "core_install_singbox" {
		return m.installSingBox(ctx, job)
	}
	target := "dae"
	if strings.HasSuffix(job.Action, "singbox") {
		target = "sing-box"
	}
	job.Target = target
	if strings.HasPrefix(job.Action, "core_check_") {
		var reply struct {
			CorePlan string `json:"corePlan"`
		}
		query := `{corePlan(target:"` + target + `")}`
		if err = m.graph(ctx, query, &reply); err != nil {
			if err.Error() == "apply_failed" {
				return errors.New("core_request_failed")
			}
			return err
		}
		var plan routercore.Plan
		if json.Unmarshal([]byte(reply.CorePlan), &plan) != nil || plan.Target != target || !routercore.ValidRevision(plan.Revision) {
			return errors.New("backend_unavailable")
		}
		if err = writeMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), storedCorePlan{plan, m.now().Unix()}); err != nil {
			return errors.New("state_failed")
		}
		// A compatibility report is useful even when it finds blocking problems.
		return nil
	}
	var saved storedCorePlan
	if readMaintenanceJSON(filepath.Join(m.stateDir, "core-plan.json"), &saved) != nil || saved.Plan.Target != target || !saved.Plan.Compatible || !routercore.ValidRevision(saved.Plan.Revision) || m.now().Unix()-saved.Checked > 600 || saved.Checked > m.now().Unix()+60 {
		return errors.New("check_required")
	}
	if err = m.save(job, "switching"); err != nil {
		return err
	}
	var result struct {
		SwitchCore int `json:"switchCore"`
	}
	query := `mutation {switchCore(target:"` + target + `",revision:"` + saved.Plan.Revision + `",operation:"` + job.ID + `")}`
	if err = m.graph(ctx, query, &result); err != nil {
		if err.Error() == "apply_failed" {
			return errors.New("core_request_failed")
		}
		return err
	}
	if result.SwitchCore != 1 {
		return errors.New("core_not_ready")
	}
	return nil
}
func (m *maintenanceManager) installSingBox(ctx context.Context, job *maintenanceJob) error {
	release, ok := routercore.Releases[runtime.GOARCH]
	if !ok {
		return errors.New("unsupported")
	}
	state, err := routercore.ReadState(m.configDir)
	if err != nil {
		return errors.New("unsafe_state")
	}
	if state.Current == "sing-box" {
		return errors.New("busy")
	}
	if err = privateDirectory(filepath.Dir(m.corePath())); err != nil {
		return errors.New("unsafe_state")
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(filepath.Dir(m.corePath()), &stat); err != nil || uint64(stat.Bavail)*uint64(stat.Bsize) < 180<<20 {
		return errors.New("no_space")
	}
	directory, err := os.MkdirTemp(m.stateDir, "core-download-")
	if err != nil {
		return errors.New("state_failed")
	}
	defer os.RemoveAll(directory)
	archive := filepath.Join(directory, "sing-box.tar.gz")
	if err = m.save(job, "downloading"); err != nil {
		return err
	}
	address := "https://github.com/SagerNet/sing-box/releases/download/v" + routercore.SingBoxVersion + "/" + release.Archive
	if err = m.fetch(ctx, address, archive, 128<<20); err != nil {
		return errors.New("download_failed")
	}
	if err = m.save(job, "verifying"); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("download_failed")
	}
	digest := sha256.New()
	_, err = io.Copy(digest, f)
	f.Close()
	if err != nil || hex.EncodeToString(digest.Sum(nil)) != release.SHA256 {
		return errors.New("checksum_failed")
	}
	target, err := os.CreateTemp(filepath.Dir(m.corePath()), ".sing-box-*")
	if err != nil {
		return errors.New("state_failed")
	}
	path := target.Name()
	target.Close()
	defer os.Remove(path)
	if err = extractSingboxPackage(archive, path); err != nil {
		return err
	}
	if err = validateSingboxELF(path, runtime.GOARCH); err != nil {
		return err
	}
	if err = os.Chmod(path, 0700); err != nil {
		return errors.New("state_failed")
	}
	output, err := m.run(ctx, path, "version")
	if err != nil || !strings.HasPrefix(string(output), "sing-box version "+routercore.SingBoxVersion+"\n") {
		return errors.New("singbox_version_mismatch")
	}
	// Install only the optional kernel module, using the router's own package
	// manager/feeds. Never replace a kernel or change feeds to force compatibility.
	if st, err := os.Stat("/dev/net/tun"); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		if err = m.save(job, "dependencies"); err != nil {
			return err
		}
		if _, err := exec.LookPath("opkg"); err == nil {
			if _, err = m.run(ctx, "opkg", "--noaction", "install", "kmod-tun"); err != nil {
				return errors.New("core_dependency_failed")
			}
			if _, err = m.run(ctx, "opkg", "install", "kmod-tun"); err != nil {
				return errors.New("core_dependency_failed")
			}
		} else if _, err := exec.LookPath("apk"); err == nil {
			if _, err = m.run(ctx, "apk", "add", "--simulate", "kmod-tun"); err != nil {
				return errors.New("core_dependency_failed")
			}
			if _, err = m.run(ctx, "apk", "add", "kmod-tun"); err != nil {
				return errors.New("core_dependency_failed")
			}
		} else {
			return errors.New("core_dependency_failed")
		}
	}
	if _, err := exec.LookPath("modprobe"); err == nil {
		_, _ = m.run(ctx, "modprobe", "tun")
	}
	if err = os.Chmod(path, 0755); err != nil {
		return errors.New("state_failed")
	}
	if err = os.Rename(path, m.corePath()); err != nil {
		return errors.New("state_failed")
	}
	parent, err := os.Open(filepath.Dir(m.corePath()))
	if err != nil {
		return errors.New("state_failed")
	}
	defer parent.Close()
	if err = parent.Sync(); err != nil {
		return errors.New("state_failed")
	}
	return nil
}

// Use only the static binary from the pinned OpenWrt package, without running
// its init scripts or installing its defaults over NaiLink's configuration.
func extractSingboxPackage(archive, destination string) error {
	inner, err := os.CreateTemp(filepath.Dir(destination), ".core-payload-*")
	if err != nil {
		return errors.New("state_failed")
	}
	path := inner.Name()
	_ = inner.Close()
	defer os.Remove(path)
	if err = extractSingbox(archive, path, "data.tar.gz"); err != nil {
		return err
	}
	return extractSingbox(path, destination, "./usr/bin/sing-box")
}
func extractSingbox(archive, destination, wanted string) error {
	f, err := os.Open(archive)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 300<<20))
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid_release")
		}
		if filepath.IsAbs(header.Name) || strings.HasPrefix(filepath.Clean(header.Name), "..") || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return errors.New("invalid_release")
		}
		if header.Name != wanted {
			continue
		}
		if found || header.Size < 1 || header.Size > 200<<20 {
			return errors.New("invalid_release")
		}
		found = true
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return errors.New("state_failed")
		}
		n, copyErr := io.Copy(out, io.LimitReader(reader, header.Size+1))
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || n != header.Size || syncErr != nil || closeErr != nil {
			return errors.New("invalid_release")
		}
	}
	if !found {
		return errors.New("invalid_release")
	}
	return nil
}
func validateSingboxELF(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return errors.New("invalid_release")
	}
	defer f.Close()
	expected := elf.EM_X86_64
	if arch == "arm64" {
		expected = elf.EM_AARCH64
	} else if arch != "amd64" {
		return errors.New("unsupported")
	}
	if f.Class != elf.ELFCLASS64 || f.Machine != expected {
		return errors.New("invalid_release")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return errors.New("invalid_release")
		}
	}
	return nil
}
