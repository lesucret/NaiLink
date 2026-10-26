// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

var routerSingBoxBinary = routercore.BinaryPath
var routerCoreRuntimeDir = "/var/run/daed/cores"
var routerCorePreflight = func(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "/usr/libexec/daed/check", "--core", target, "--check")
	if err := command.Run(); err != nil {
		return errors.New("core_environment_unsupported")
	}
	return nil
}

func routerSingBoxInstalled() bool {
	st, err := os.Lstat(routerSingBoxBinary)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 && st.Size() > 0 && st.Size() < 200<<20
}
func routerSingBoxVersion(ctx context.Context) error {
	if !routerSingBoxInstalled() {
		return errors.New("singbox_not_installed")
	}
	// Output is read through a limit. Never surface a core's arbitrary stderr.
	c := exec.CommandContext(ctx, routerSingBoxBinary, "version")
	var output routerCoreBoundedWriter
	c.Stdout = &output
	c.Stderr = io.Discard
	if err := c.Run(); err != nil || !strings.HasPrefix(string(output.data), "sing-box version "+routercore.SingBoxVersion+"\n") {
		return errors.New("singbox_version_mismatch")
	}
	return nil
}

type routerCoreBoundedWriter struct{ data []byte }

func (w *routerCoreBoundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.data) < 65536 {
		limit := 65536 - len(w.data)
		if len(p) > limit {
			p = p[:limit]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

type sbLogWriter struct {
	mu      sync.Mutex
	pending []byte
	log     *logrus.Logger
}

func (w *sbLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		level := logrus.InfoLevel
		prefix := line
		if len(prefix) > 20 {
			prefix = prefix[:20]
		}
		switch {
		case strings.Contains(prefix, "ERROR") || strings.Contains(prefix, "FATAL"):
			level = logrus.ErrorLevel
		case strings.Contains(prefix, "WARN"):
			level = logrus.WarnLevel
		case strings.Contains(prefix, "DEBUG") || strings.Contains(prefix, "TRACE"):
			level = logrus.DebugLevel
		}
		w.log.WithField("core", "sing-box").Log(level, line)
	}
	if len(w.pending) > 32768 {
		w.pending = w.pending[:0]
		w.log.Warn("Oversized sing-box log record omitted")
	}
	return len(data), nil
}
func newCorePorts() (sbPorts, []net.Listener, error) {
	var listeners []net.Listener
	ports := sbPorts{}
	for _, target := range []*int{&ports.Mixed, &ports.API, &ports.DNS} {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return ports, nil, err
		}
		listeners = append(listeners, l)
		*target = l.Addr().(*net.TCPAddr).Port
	}
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		for _, l := range listeners {
			_ = l.Close()
		}
		return ports, nil, err
	}
	ports.Secret = hex.EncodeToString(secret[:])
	return ports, listeners, nil
}
func routerCheckSingBox(ctx context.Context, conf *daeConfig.Config, dirs []string, transparent bool) error {
	profile, _, err := singBoxConfig(conf, dirs, sbPorts{Mixed: 39001, API: 39002, DNS: 39003, Secret: "offline-configuration-check"}, transparent)
	if err != nil {
		return err
	}
	return routerCheckSingBoxProfile(ctx, profile)
}

func routerCheckSingBoxProfile(ctx context.Context, profile sbObject) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := routerSingBoxVersion(ctx); err != nil {
		return err
	}
	var err error
	if err = routercore.PrivateDir(routerCoreRuntimeDir); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(routerCoreRuntimeDir, "check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	if err = routercore.Write(path, data); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, routerSingBoxBinary, "check", "--disable-color", "-c", path)
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return errors.New("core_configuration_check_timeout")
		}
		return errors.New("singbox_configuration_invalid")
	}
	return nil
}

type routerSingBoxSession struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	done        chan error
	exit        chan struct{}
	ports       sbPorts
	dir         string
	conf        *daeConfig.Config
	dirs        []string
	transparent bool
	log         *logrus.Logger
	replacement *routerSingBoxSession
	owned       bool
	ready       atomic.Bool
	statsMu     sync.Mutex
	stats       *RuntimeOverview
	samples     []RuntimeTrafficSample
}

func newRouterSingBoxSession(ctx context.Context, log *logrus.Logger, conf *daeConfig.Config, dirs []string, transparent bool, profileHooks ...func(sbObject, sbPorts)) (routerCoreSession, error) {
	if err := routerSingBoxVersion(ctx); err != nil {
		return nil, err
	}
	ports, reserved, err := newCorePorts()
	if err != nil {
		return nil, errors.New("core_ports_unavailable")
	}
	defer func() {
		for _, l := range reserved {
			_ = l.Close()
		}
	}()
	profile, _, err := singBoxConfig(conf, dirs, ports, transparent)
	if err != nil {
		return nil, err
	}
	for _, hook := range profileHooks {
		hook(profile, ports)
	}
	if err = routercore.PrivateDir(routerCoreRuntimeDir); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(routerCoreRuntimeDir, "active-")
	if err != nil {
		return nil, err
	}
	s := &routerSingBoxSession{done: make(chan error, 1), exit: make(chan struct{}), ports: ports, dir: dir, conf: conf, dirs: dirs, transparent: transparent, log: log}
	data, err := json.Marshal(profile)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err = routercore.Write(path, data); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	check := exec.CommandContext(ctx, routerSingBoxBinary, "check", "--disable-color", "-c", path)
	if err = check.Run(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, errors.New("singbox_configuration_invalid")
	}
	if transparent && conf != EmptyConfig {
		if err = routerClaimSingboxNetwork(dirs[0], path); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		s.owned = true
	}
	cleanupFailure := func(cause error) error {
		if s.owned {
			if cleanupErr := routerReleaseSingboxNetwork(dirs[0]); cleanupErr != nil {
				return errors.New("core_cleanup_failed")
			}
		}
		_ = os.RemoveAll(dir)
		return cause
	}
	s.cmd = exec.Command(routerSingBoxBinary, "run", "--disable-color", "-D", dir, "-c", path)
	// TUN and routes are native netlink operations. The manager owns fw4
	// integration; hide external tools from this optional child so its automatic
	// fw4 hook cannot replace shared policies or reload other plugins' rules.
	emptyPath := filepath.Join(dir, "bin")
	if err = os.Mkdir(emptyPath, 0700); err != nil {
		return nil, cleanupFailure(err)
	}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			s.cmd.Env = append(s.cmd.Env, entry)
		}
	}
	s.cmd.Env = append(s.cmd.Env, "PATH="+emptyPath)
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	writer := &sbLogWriter{log: log}
	s.cmd.Stdout = writer
	s.cmd.Stderr = writer
	for _, l := range reserved {
		_ = l.Close()
	}
	if err = s.cmd.Start(); err != nil {
		return nil, cleanupFailure(errors.New("core_start_failed"))
	}
	if s.owned {
		if err = routerRecordSingboxProcess(dirs[0], s.cmd.Process.Pid, path); err != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
			return nil, cleanupFailure(err)
		}
	}
	go func() { err := s.cmd.Wait(); s.done <- err; close(s.exit) }()
	return s, nil
}
func (s *routerSingBoxSession) current() *routerSingBoxSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replacement != nil {
		return s.replacement.current()
	}
	return s
}
func (s *routerSingBoxSession) Done() <-chan error { return s.current().done }
func (s *routerSingBoxSession) api(ctx context.Context, path string, value any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+routercore.SocksAddress(s.ports.API)+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.ports.Secret)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		return errors.New("core_not_ready")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("core_not_ready")
	}
	if value != nil && json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(value) != nil {
		return errors.New("core_api_invalid")
	}
	return nil
}
func (s *routerSingBoxSession) Ready(ctx context.Context) error {
	s = s.current()
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.api(ctx, "/version", nil) == nil {
			if !s.transparent || s.conf == EmptyConfig {
				s.ready.Store(true)
				return nil
			}
			if _, err := net.InterfaceByName(routercore.TunName); err == nil {
				if err = routerRecordRedirectTable(s.dirs[0]); err != nil {
					return err
				}
				if err = routerVerifySingBox(ctx, s); err == nil {
					s.ready.Store(true)
					return nil
				} else {
					return err
				}
			}
		}
		select {
		case <-s.exit:
			return errors.New("core_start_failed")
		case <-ctx.Done():
			return errors.New("core_not_ready")
		case <-ticker.C:
		}
	}
}
func (s *routerSingBoxSession) Stop(ctx context.Context) error {
	s = s.current()
	s.ready.Store(false)
	select {
	case <-s.exit:
	default:
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		select {
		case <-s.exit:
		case <-timer.C:
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-s.exit:
			case <-ctx.Done():
				return errors.New("core_cleanup_failed")
			}
		case <-ctx.Done():
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			return errors.New("core_cleanup_failed")
		}
	}
	if s.owned {
		if err := routerReleaseSingboxNetwork(s.dirs[0]); err != nil {
			return err
		}
		s.owned = false
	}
	return os.RemoveAll(s.dir)
}
func (s *routerSingBoxSession) Reload(message *ReloadMessage) error {
	old := s.current()
	conf := message.Config
	var rollback func() error
	var err error
	if message.Geodata != nil {
		conf = old.conf
		rollback, err = routerBeginGeodata(message.Geodata)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = routerCheckSingBox(ctx, conf, old.dirs, old.transparent); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	if err = old.Stop(ctx); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	candidate, err := newRouterSingBoxSession(ctx, old.log, conf, old.dirs, old.transparent)
	if err == nil {
		err = candidate.Ready(ctx)
	}
	if err == nil && message.Geodata != nil {
		err = routerCommitGeodata(message.Geodata)
	}
	if err == nil {
		s.mu.Lock()
		s.replacement = candidate.(*routerSingBoxSession)
		s.mu.Unlock()
		return nil
	}
	if candidate != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupErr := candidate.Stop(cleanup)
		stop()
		if cleanupErr != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	if rollback != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.New("core_rollback_failed")
		}
	}
	restoreCtx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	defer stop()
	restored, restoreErr := newRouterSingBoxSession(restoreCtx, old.log, old.conf, old.dirs, old.transparent)
	if restoreErr == nil {
		restoreErr = restored.Ready(restoreCtx)
	}
	if restoreErr != nil {
		if restored != nil {
			_ = restored.Stop(restoreCtx)
		}
		return errors.New("core_rollback_failed")
	}
	s.mu.Lock()
	s.replacement = restored.(*routerSingBoxSession)
	s.mu.Unlock()
	return errors.New("core_reload_rolled_back")
}
func (s *routerSingBoxSession) dial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer, err := proxy.SOCKS5("tcp", routercore.SocksAddress(s.ports.Mixed), &proxy.Auth{User: "nailink", Password: s.ports.Secret}, &net.Dialer{Timeout: 8 * time.Second})
	if err != nil {
		return nil, err
	}
	return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
}
func RouterCoreDialContext(ctx context.Context, network, address string) (net.Conn, bool, error) {
	m := currentRouterCoreManager()
	if m == nil {
		return nil, false, nil
	}
	m.mu.RLock()
	active := m.active
	current := m.state.Current
	m.mu.RUnlock()
	if current != "sing-box" {
		return nil, false, nil
	}
	s, ok := active.(*routerSingBoxSession)
	if !ok {
		return nil, true, errors.New("core_not_ready")
	}
	conn, err := s.current().dial(ctx, network, address)
	return conn, true, err
}
func routerVerifySingBox(ctx context.Context, s *routerSingBoxSession) error {
	// Validate real DNS over the private listener, then the configured HTTP target
	// over this core's authenticated SOCKS listener; API availability is insufficient.
	if len(s.conf.Global.TcpCheckUrl) == 0 {
		return errors.New("missing_check_url")
	}
	target := s.conf.Global.TcpCheckUrl[0]
	parsed, parseErr := url.Parse(target)
	if parseErr != nil || parsed.Hostname() == "" {
		return errors.New("invalid_check_url")
	}
	name := parsed.Hostname()
	if net.ParseIP(name) != nil {
		name = "example.com"
	}
	qtype := uint16(dnsmessage.TypeA)
	if s.conf.Dns.IpVersionPrefer == 6 {
		qtype = dnsmessage.TypeAAAA
	}
	question := new(dnsmessage.Msg)
	question.SetQuestion(dnsmessage.Fqdn(name), qtype)
	dnsClient := &dnsmessage.Client{Net: "udp", Timeout: 4 * time.Second}
	answer, _, dnsErr := dnsClient.ExchangeContext(ctx, question, routercore.SocksAddress(s.ports.DNS))
	if dnsErr != nil || answer == nil || answer.Id != question.Id || answer.Rcode != dnsmessage.RcodeSuccess || len(answer.Answer) == 0 || len(answer.Question) != 1 || answer.Question[0] != question.Question[0] {
		return errors.New("core_dns_failed")
	}
	transport := &http.Transport{Proxy: nil, DialContext: s.dial, TLSHandshakeTimeout: 6 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, "HEAD", target, nil)
	if err != nil {
		return errors.New("invalid_check_url")
	}
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return errors.New("core_connectivity_failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 500 {
		return errors.New("core_connectivity_failed")
	}
	return nil
}
