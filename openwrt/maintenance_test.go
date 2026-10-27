package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daeuniverse/dae/pkg/geodata"
	"google.golang.org/protobuf/proto"
)

func maintenanceFixture(t *testing.T) *maintenanceManager {
	t.Helper()
	root := t.TempDir()
	m := newMaintenance(filepath.Join(root, "config"))
	m.stateDir = filepath.Join(root, "state")
	m.versionFile = filepath.Join(root, "version")
	m.lockFile = filepath.Join(root, "maintenance.lock")
	m.installLock = filepath.Join(root, "install.lock")
	m.initScript = filepath.Join(root, "init")
	os.Mkdir(m.stateDir, 0700)
	os.Mkdir(m.configDir, 0700)
	os.WriteFile(m.versionFile, []byte("0.6.32-1\n"), 0600)
	m.now = func() time.Time { return time.Unix(1800000000, 0) }
	m.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == m.initScript {
			return nil, nil
		}
		return maintenanceCommand(ctx, args...)
	}
	return m
}
func releaseFixture(tag string, draft bool) map[string]any {
	return map[string]any{"tag_name": tag, "draft": draft, "assets": []map[string]string{{"name": "install.sh"}, {"name": "SHA256SUMS"}, {"name": "manifest.tsv"}}}
}
func fixtureFetch(files map[string][]byte) func(context.Context, string, string, int64) error {
	return func(_ context.Context, address, dest string, limit int64) error {
		data, ok := files[address]
		if !ok || len(data) > int(limit) {
			return errors.New("fixture_download_failure")
		}
		return os.WriteFile(dest, data, 0600)
	}
}
func runFixtureJob(t *testing.T, m *maintenanceManager, action string) maintenanceStatus {
	t.Helper()
	s := m.start(action)
	if !s.Success {
		t.Fatalf("start: %+v", s)
	}
	if err := m.worker(context.Background(), s.Job.ID); err != nil {
		t.Fatal(err)
	}
	return m.status()
}

func TestMaintenanceVersionSelectionAndPersistentJob(t *testing.T) {
	m := maintenanceFixture(t)
	data, _ := json.Marshal([]any{releaseFixture("openwrt-v9.0.0", true), releaseFixture("other-v10", false), releaseFixture("openwrt-v0.6.33", false), releaseFixture("openwrt-v0.6.9", false)})
	m.fetch = fixtureFetch(map[string][]byte{"https://api.github.com/repos/slobys/NaiLink/releases?per_page=10": data})
	result := runFixtureJob(t, m, "check")
	if result.Latest == nil || result.Latest.Version != "0.6.33" || result.Job.State != "success" {
		t.Fatalf("%+v", result)
	}
	// A new process/request sees the same task and checked version.
	copy := *m
	if copy.status().Job.ID != result.Job.ID {
		t.Fatal("task only existed in request memory")
	}
	if m.start("update; rm -rf /").Code != "invalid_action" {
		t.Fatal("arbitrary action accepted")
	}
	for _, pair := range [][2]string{{"0.6.33", "0.6.32-1"}, {"0.7.0", "0.6.99"}, {"1.0.0", "0.99.9"}} {
		if !versionNewer(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
	for _, pair := range [][2]string{{"0.6.32", "0.6.32-1"}, {"0.6.9", "0.6.32"}, {"evil", "0.6.32"}} {
		if versionNewer(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
}

func TestMaintenanceMutualExclusionAndInterruptedRecovery(t *testing.T) {
	m := maintenanceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	m.fetch = func(_ context.Context, _ string, dest string, _ int64) error {
		close(entered)
		<-release
		data, _ := json.Marshal([]any{releaseFixture("openwrt-v0.6.33", false)})
		return os.WriteFile(dest, data, 0600)
	}
	if !m.start("check").Success {
		t.Fatal("queue failed")
	}
	done := make(chan error, 1)
	go func() { done <- m.worker(context.Background(), m.status().Job.ID) }()
	<-entered
	if m.start("geodata").Code != "busy" || m.status().Job.State != "running" {
		t.Fatal("running worker was replaced or reported interrupted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	old := maintenanceJob{ID: "fixture", State: "running", Updated: m.now().Unix()}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), old)
	if job := m.status().Job; job.State != "error" || job.Code != "interrupted" {
		t.Fatal("dead worker left infinite loading")
	}
	old.State = "queued"
	old.Updated = m.now().Unix()
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), old)
	if m.start("check").Code != "busy" {
		t.Fatal("freshly queued worker overwritten")
	}
}

func TestMaintenanceInstallerChecksumAndPreservedData(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			m := maintenanceFixture(t)
			writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.33", "openwrt-v0.6.33", m.now().Unix()})
			sentinel := filepath.Join(m.configDir, "nodes.db")
			os.WriteFile(sentinel, []byte("existing nodes and settings"), 0600)
			script := []byte("#!/bin/sh\nrelease=openwrt-v0.6.33\nprintf '0.6.33-1\\n' > " + m.versionFile + "\n")
			hash := sha256.Sum256(script)
			sums := []byte(hex.EncodeToString(hash[:]) + "  install.sh\n")
			if corrupt {
				script = append(script, []byte("# corrupted")...)
			}
			base := "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.33/"
			m.fetch = fixtureFetch(map[string][]byte{base + "install.sh": script, base + "SHA256SUMS": sums})
			result := runFixtureJob(t, m, "update")
			if corrupt {
				if result.Job.Code != "checksum_failed" || m.current() != "0.6.32-1" {
					t.Fatalf("unsafe corrupt install: %+v", result.Job)
				}
			} else if result.Job.State != "success" || m.current() != "0.6.33-1" {
				t.Fatalf("install failed: %+v", result.Job)
			}
			data, _ := os.ReadFile(sentinel)
			if string(data) != "existing nodes and settings" {
				t.Fatal("user data changed")
			}
		})
	}
}

func TestMaintenanceUnsafeStateOldTargetAndMissingWorker(t *testing.T) {
	m := maintenanceFixture(t)
	if m.start("update").Code != "check_required" {
		t.Fatal("unchecked update accepted")
	}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "latest.json"), releaseInfo{"0.6.31", "openwrt-v0.6.31", m.now().Unix()})
	if m.start("update").Code != "already_latest" {
		t.Fatal("downgrade accepted")
	}
	m.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	if m.start("check").Code != "worker_unavailable" || m.status().Job.State != "error" {
		t.Fatal("missing worker reported running")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Remove(filepath.Join(m.stateDir, "job.json"))
	os.Symlink(outside, filepath.Join(m.stateDir, "job.json"))
	var job maintenanceJob
	if readMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), &job) == nil {
		t.Fatal("symlinked state accepted")
	}
	os.RemoveAll(m.stateDir)
	os.Symlink(filepath.Dir(outside), m.stateDir)
	if m.start("check").Code != "unsafe_state" {
		t.Fatal("symlinked state directory accepted")
	}
}

func geodataFixture(t *testing.T, m *maintenanceManager) map[string][]byte {
	t.Helper()
	ip, _ := proto.Marshal(&geodata.GeoIPList{Entry: []*geodata.GeoIP{{CountryCode: "CN", Cidr: []*geodata.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}})
	site, _ := proto.Marshal(&geodata.GeoSiteList{Entry: []*geodata.GeoSite{{CountryCode: "CN", Domain: []*geodata.Domain{{Type: geodata.Domain_RootDomain, Value: "example.invalid"}}}}})
	meta := map[string]any{"tag_name": "20261025", "assets": []map[string]string{{"name": "geoip.dat"}, {"name": "geosite.dat"}, {"name": "geoip.dat.sha256sum"}, {"name": "geosite.dat.sha256sum"}}}
	data, _ := json.Marshal(meta)
	files := map[string][]byte{"https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest": data}
	base := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/20261025/"
	for name, content := range map[string][]byte{"geoip.dat": ip, "geosite.dat": site} {
		hash := sha256.Sum256(content)
		files[base+name] = content
		files[base+name+".sha256sum"] = []byte(hex.EncodeToString(hash[:]) + "  " + name + "\n")
	}
	m.fetch = fixtureFetch(files)
	return files
}

func TestMaintenanceGeodataAtomicActivationAndFailurePaths(t *testing.T) {
	for _, mode := range []string{"success", "download_failed", "invalid_geodata", "custom_geodata", "apply_failed", "apply_unconfirmed", "pending_configuration"} {
		t.Run(mode, func(t *testing.T) {
			m := maintenanceFixture(t)
			files := geodataFixture(t, m)
			directory := filepath.Join(m.configDir, "geodata")
			os.Mkdir(directory, 0700)
			old := filepath.Join(directory, "data-1")
			os.Mkdir(old, 0700)
			for _, name := range []string{"geoip.dat", "geosite.dat"} {
				os.WriteFile(filepath.Join(old, name), []byte("old-"+name), 0600)
			}
			os.Symlink("data-1", filepath.Join(directory, "current"))
			base := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/20261025/"
			if mode == "download_failed" {
				delete(files, base+"geosite.dat")
			}
			if mode == "invalid_geodata" {
				html := []byte("<html>error</html>")
				hash := sha256.Sum256(html)
				files[base+"geosite.dat"] = html
				files[base+"geosite.dat.sha256sum"] = []byte(hex.EncodeToString(hash[:]) + "  geosite.dat\n")
			}
			if mode == "custom_geodata" {
				os.WriteFile(filepath.Join(m.configDir, "geoip.dat"), []byte("custom"), 0600)
			}
			activated := false
			m.graph = func(_ context.Context, query string, out any) error {
				if strings.HasPrefix(query, "query") {
					data := []byte(fmt.Sprintf(`{"general":{"dae":{"running":true,"modified":%t}}}`, mode == "pending_configuration"))
					return json.Unmarshal(data, out)
				}
				if !strings.Contains(query, "activateGeodata(") || strings.Contains(query, "run(") {
					t.Fatal("ordinary config apply used")
				}
				target := strings.Split(strings.Split(query, `target:"`)[1], `"`)[0]
				if _, err := os.ReadFile(filepath.Join(directory, target, "geoip.dat")); err != nil {
					t.Fatal("activation before both downloads completed")
				}
				if _, err := os.ReadFile(filepath.Join(directory, target, "geosite.dat")); err != nil {
					t.Fatal("partial pair")
				}
				activated = true
				if mode == "apply_failed" {
					return errors.New("apply_failed")
				}
				if err := switchGeodata(directory, target); err != nil {
					return err
				}
				if mode != "apply_unconfirmed" {
					if err := writeMaintenanceJSON(filepath.Join(directory, "activation.json"), maintenanceGeodataReceipt{target, "data-1", "applied"}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := writeMaintenanceJSON(filepath.Join(directory, "activation.json"), maintenanceGeodataReceipt{target, "data-1", "applying"}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "apply_unconfirmed" {
					return errors.New("backend_unavailable")
				}
				return nil
			}
			result := runFixtureJob(t, m, "geodata")
			current, _ := os.Readlink(filepath.Join(directory, "current"))
			if mode == "success" || mode == "pending_configuration" {
				if result.Job.State != "success" || current == "data-1" || !activated {
					t.Fatalf("%+v", result.Job)
				}
			} else {
				if result.Job.State != "error" || result.Job.Code != mode {
					t.Fatalf("wanted %s, got %+v", mode, result.Job)
				}
				if mode != "apply_unconfirmed" && current != "data-1" {
					t.Fatal("old pair lost after failure")
				}
				if mode == "apply_unconfirmed" {
					if _, err := os.Stat(filepath.Join(directory, "current", "geosite.dat")); err != nil {
						t.Fatal("deleted uncertain in-flight batch")
					}
				}
			}
			for _, name := range []string{"geoip.dat", "geosite.dat"} {
				data, _ := os.ReadFile(filepath.Join(old, name))
				if string(data) != "old-"+name {
					t.Fatal("old asset changed")
				}
			}
		})
	}
}

type maintenanceTransport func(*http.Request) (*http.Response, error)

func (f maintenanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMaintenanceLargeTransferBudgetAndCancellation(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, test := range []struct { name string; limit int64; minimum, maximum time.Duration }{
		{"metadata", 512 << 10, 2 * time.Minute, 3 * time.Minute},
		{"optional core", 128 << 20, 9 * time.Minute, 10 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining < test.minimum || remaining > test.maximum { t.Fatal("incorrect transfer budget", remaining) }
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("verified by caller")), Request: r}, nil
			})
			if err := maintenanceDownload(context.Background(), "https://github.com/test", filepath.Join(t.TempDir(), "download"), test.limit); err != nil { t.Fatal(err) }
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	name := filepath.Join(t.TempDir(), "cancelled")
	if maintenanceDownload(ctx, "https://github.com/test", name, 128<<20) == nil { t.Fatal("caller cancellation ignored") }
	if _, err := os.Stat(name); !os.IsNotExist(err) { t.Fatal("cancelled download changed destination") }
}
func TestMaintenanceDownloadBoundsOriginsAndOutput(t *testing.T) {
	for _, address := range []string{"http://github.com/test", "https://evil.test/test", "https://github.com:444/test", "https://user@github.com/test"} {
		if maintenanceDownload(context.Background(), address, filepath.Join(t.TempDir(), "x"), 10) == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 50))), ContentLength: -1, Request: r}, nil
	})
	name := filepath.Join(t.TempDir(), "download")
	if maintenanceDownload(context.Background(), "https://github.com/test", name, 10) == nil {
		t.Fatal("oversize download accepted")
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("partial file retained")
	}
	http.DefaultTransport = maintenanceTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://evil.test"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if maintenanceDownload(context.Background(), "https://github.com/test", name, 10) == nil {
		t.Fatal("insecure redirect accepted")
	}
	var out boundedOutput
	out.Write([]byte(strings.Repeat("a", 100000)))
	out.Write([]byte("last error"))
	if len(out.data) != 65536 || !strings.HasSuffix(string(out.data), "last error") {
		t.Fatal("unbounded installer output")
	}
	if maintenanceCode(errors.New("password=private")) != "operation_failed" {
		t.Fatal("raw error leaked")
	}
}

func TestMaintenancePublishedGeodata(t *testing.T) {
	directory := os.Getenv("DAED_TEST_GEODATA_DIR")
	if directory == "" {
		t.Skip("Set DAED_TEST_GEODATA_DIR for checksum-verified upstream data")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		sums, err := os.ReadFile(filepath.Join(directory, name+".sha256sum"))
		if err != nil {
			t.Fatal(err)
		}
		digest, err := checksumFor(sums, name)
		if err != nil {
			t.Fatal(err)
		}
		if err = verifyChecksum(filepath.Join(directory, name), digest); err != nil {
			t.Fatal(err)
		}
		if err = validateGeodata(filepath.Join(directory, name), name); err != nil {
			t.Fatalf("upstream %s: %v", name, err)
		}
	}
}

func TestMaintenanceClockCorrectionAndGeodataReceipt(t *testing.T) {
	m := maintenanceFixture(t)
	job := maintenanceJob{ID: strings.Repeat("a", 24), Action: "check", State: "queued", Updated: m.now().Unix() + 3600}
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	if got := m.status(); got.Job.Code != "interrupted" {
		t.Fatal("future queue never recovered")
	}
	directory := filepath.Join(m.configDir, "geodata")
	os.Mkdir(directory, 0700)
	for _, batch := range []string{"data-1", "data-2"} {
		os.Mkdir(filepath.Join(directory, batch), 0700)
		writeMaintenanceJSON(filepath.Join(directory, batch, "version.json"), releaseInfo{batch, batch, 1})
	}
	os.Symlink("data-2", filepath.Join(directory, "current"))
	job.Action, job.State, job.Code, job.Batch = "geodata", "error", "apply_unconfirmed", "data-2"
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	receipt := maintenanceGeodataReceipt{"data-2", "data-1", "applying"}
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	got := m.status()
	if got.Job.Code != "apply_unconfirmed" || got.Geodata.Version != "data-1" {
		t.Fatal("unconfirmed batch shown as active")
	}
	if m.start("check").Code != "apply_unconfirmed" {
		t.Fatal("in-flight activation overwritten")
	}
	receipt.State = "applied"
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	got = m.status()
	if got.Job.State != "success" || got.Job.Code != "recovered" || got.Geodata.Version != "data-2" {
		t.Fatal("lost success response did not recover")
	}
	job.State, job.Code = "error", "apply_unconfirmed"
	writeMaintenanceJSON(filepath.Join(m.stateDir, "job.json"), job)
	receipt.State = "rolled_back"
	writeMaintenanceJSON(filepath.Join(directory, "activation.json"), receipt)
	if got = m.status(); got.Job.Code != "apply_failed" {
		t.Fatal("rollback was reported as success")
	}
}

func TestMaintenanceCommandAllowsRecoveryTrapAndStopsChildren(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "installer")
	os.WriteFile(script, []byte("#!/bin/sh\ntrap 'echo restored > \"$1/recovered\"; exit 1' TERM\nsleep 60 &\necho $! > \"$1/child\"\nwait\n"), 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_, err := maintenanceCommand(ctx, "/bin/sh", script, directory)
	if err == nil {
		t.Fatal("interruption lost")
	}
	if _, err = os.Stat(filepath.Join(directory, "recovered")); err != nil {
		t.Fatal("installer recovery trap was killed")
	}
	data, _ := os.ReadFile(filepath.Join(directory, "child"))
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if pid <= 0 {
		t.Fatal("missing child")
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err == nil && !strings.Contains(string(data), "State:\tZ") && syscall.Kill(pid, 0) == nil {
		t.Fatal("orphan installer child is still running")
	}
}
