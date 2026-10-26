#!/usr/bin/env python3
"""Apply the reviewed router backend additions to the pinned wing working copy.

The Git submodule revision is unchanged. Sources live here in the parent repo
and are included in the release source archive. Unknown local edits are refused.
"""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import tempfile

HERE = Path(__file__).resolve().parent
ORIGINAL = "ae8c0126a163fce0d7c1abea226579c69c9784f65e1e7f95bd1a8e903d450779"
TRANSACTION_DEFER = '''defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()'''


def router_transactions(text):
    text = re.sub(r'(func Update\([^\n]+\)) \(\*Resolver, error\)', r'\1 (result *Resolver, err error)', text)
    text = text.replace(') (int32, error) {', ') (n int32, err error) {')
    return text.replace(TRANSACTION_DEFER, 'defer db.FinishTransaction(tx, &err)')


def router_auth(text):
    text = text.replace('[]string{cfgDir}', '[]string{cfgDir, cfgDir + "/geodata/current"}')
    text = (text.replace('ctx := context.Background()', 'ctx := r.Context()')
            .replace('db.DB(context.TODO()).Model(&db.User{})', 'db.DB(r.Context()).Model(&db.User{})')
            .replace('err == nil && time.Now().Before(expireAt.Time)', 'err == nil && expireAt != nil && time.Now().Before(expireAt.Time)'))
    text = text.replace('\t\ttx2.Commit()', '\t\tif err2 := tx2.Commit().Error; err2 != nil { return fmt.Errorf("%w; %v", err, err2) }')
    if 'mux.HandleFunc("/health/ready"' not in text:
        text = text.replace('mux := http.NewServeMux()', 'mux := http.NewServeMux()\n\t\t\tmux.HandleFunc("/health/ready", routerReadinessHandler)', 1)
    return text.replace('\ttx.Commit()\n\treturn nil', '\treturn tx.Commit().Error')


def router_lifecycle(text):
    if '*RouterGeodataTransaction' not in text:
        text = text.replace('Callback chan<- error', 'Callback chan<- error\n\tGeodata *RouterGeodataTransaction', 1)
    if 'routerSetGeodataRoot(externGeoDataDirs)' not in text:
        text = text.replace('defer close(GracefullyExit)', 'defer close(GracefullyExit)\n\trouterSetGeodataRoot(externGeoDataDirs)', 1)
    if 'routerRecoverGeodata()' not in text:
        text = text.replace('routerSetGeodataRoot(externGeoDataDirs)', 'routerSetGeodataRoot(externGeoDataDirs)\n\tif err := routerRecoverGeodata(); err != nil { return err }', 1)
    if 'routerBeginGeodata(newConf.Geodata)' not in text:
        text = text.replace('newConf.Callback <- nil', 'if newConf.Geodata != nil { _, err := routerBeginGeodata(newConf.Geodata); newConf.Callback <- err } else { newConf.Callback <- nil }', 1)
    if 'routerCommitGeodata(newConf.Geodata)' not in text:
        text = text.replace('_, err := routerBeginGeodata(newConf.Geodata); newConf.Callback <- err', '_, err := routerBeginGeodata(newConf.Geodata); if err == nil { err = routerCommitGeodata(newConf.Geodata) }; newConf.Callback <- err', 1)
    if 'var pendingGeodata *RouterGeodataTransaction' not in text:
        text = text.replace('var chCallback chan<- error', 'var chCallback chan<- error\n\tvar pendingGeodata *RouterGeodataTransaction', 1)
        text = text.replace('chCallback = newReloadMsg.Callback', 'chCallback = newReloadMsg.Callback\n\t\t\tpendingGeodata = newReloadMsg.Geodata', 1)
        text = text.replace('chCallback <- errReload', 'if errReload == nil && pendingGeodata != nil { errReload = routerCommitGeodata(pendingGeodata) }; pendingGeodata = nil\n\t\t\t\tchCallback <- errReload', 1)
    if 'routerBeginGeodata(newReloadMsg.Geodata)' not in text:
        text = text.replace('newConf := newReloadMsg.Config', '''newConf := newReloadMsg.Config
            var rollbackGeodata func() error
            if newReloadMsg.Geodata != nil {
                var geoErr error
                rollbackGeodata, geoErr = routerBeginGeodata(newReloadMsg.Geodata)
                if geoErr != nil { newReloadMsg.Callback <- geoErr; continue }
                newConf = conf
            }''', 1)
        text = text.replace('// Load last config back.', '''// Restore the old asset pointer before the fallback reads the old config.
                if rollbackGeodata != nil { if geoErr := rollbackGeodata(); geoErr != nil { log.Error("[Reload] Cannot restore previous geodata") } }
                // Load last config back.''', 1)
    text = text.replace('"sync"', '"sync"\n\t"sync/atomic"') if '"sync/atomic"' not in text else text
    text = text.replace('var c *control.ControlPlane', 'var currentControlPlane atomic.Pointer[control.ControlPlane]')
    text = text.replace('func ControlPlane() (*control.ControlPlane, error) {\n\tif c == nil', 'func ControlPlane() (*control.ControlPlane, error) {\n\tc := currentControlPlane.Load()\n\tif c == nil')
    text = text.replace('c, err = newControlPlane(log, nil, nil, conf, externGeoDataDirs)', 'c, err := newControlPlane(log, nil, nil, conf, externGeoDataDirs)')
    if 'defer currentControlPlane.Store(nil)' not in text:
        text = text.replace('\t// Serve tproxy TCP/UDP server util signals.', '\tcurrentControlPlane.Store(c)\n\tdefer currentControlPlane.Store(nil)\n\n\t// Serve tproxy TCP/UDP server util signals.')
    # The serve goroutines must retain their own plane/logger, rather than read
    # variables which the reload loop replaces while an old instance exits.
    text = text.replace('go func() {\n\t\treadyChan :=', 'go func(c *control.ControlPlane, log *logrus.Logger, port uint16) {\n\t\tvar err error\n\t\treadyChan :=', 1)
    text = text.replace('readyChan, conf.Global.TproxyPort)', 'readyChan, port)', 1)
    text = text.replace('\t}()\n\treloading := false', '\t}(c, log, conf.Global.TproxyPort)\n\treloading := false', 1)
    text = text.replace('go func() {\n\t\t\t\t\tif err := c.Serve', 'go func(c *control.ControlPlane, log *logrus.Logger) {\n\t\t\t\t\tif err := c.Serve', 1)
    text = text.replace('\t\t\t\t}()\n\t\t\t\t<-readyChan', '\t\t\t\t}(c, log)\n\t\t\t\t<-readyChan', 1)
    if '\t\t\trouterPublishControlPlane(c, conf)' not in text:
        text = text.replace('\t\t\tc = newC\n\t\t\tconf =', '\t\t\tc = newC\n\t\t\tcurrentControlPlane.Store(c)\n\t\t\tconf =')
    text = text.replace('currentControlPlane.Store(c)', 'routerPublishControlPlane(c, conf)')
    text = text.replace('routerPublishControlPlane(c, conf)\n\t\t\tconf = newConf', 'conf = newConf\n\t\t\trouterPublishControlPlane(c, conf)')
    text = text.replace('oldC.Close()', 'routerCloseControlPlane(oldC)').replace('c.Close()', 'routerCloseControlPlane(c)')
    if 'routerSaveRestartDNS(log, c, conf)' not in text:
        text = text.replace('\tif e := routerCloseControlPlane(c);', '\trouterSaveRestartDNS(log, c, conf)\n\tif e := routerCloseControlPlane(c);')
    if 'routerRestoreRestartDNS(log, conf, dnsCache)' not in text:
        text = text.replace('\n\t// Print configuration.', '\n\tdnsCache = routerRestoreRestartDNS(log, conf, dnsCache)\n\t// Print configuration.', 1)
    if 'routerMarkServing(c)' not in text:
        text = text.replace('<-readyChan', '<-readyChan\n\t\t\trouterMarkServing(c)')
    return text.replace('\n\t\t\tWaitForNetwork(log)', '\n\t\t\trouterWaitForNetwork(log)')


def router_control_dial(text):
    begin = text.index('func (c *ControlPlane) routeDial(')
    return text[:begin] + '''func (c *ControlPlane) routeDial(ctx context.Context, p *proxyDialParam) (netproxy.Conn, *proxyDialResult, error) {
    return c.routerRouteDial(ctx, p)
}
'''


def router_dns_recovery(text):
    if 'routerReplayFailed atomic.Bool' not in text:
        text = text.replace('type dnsControllerStore struct {', 'type dnsControllerStore struct {\n\trouterReplayFailed atomic.Bool')
    text = text.replace('c.rememberDnsKnowledge(dnsCacheBaseKey(k), v.OriginalDeadline)\n\t\tc.triggerBpfUpdateIfNeeded(v, now)',
                        'c.rememberDnsKnowledge(dnsCacheBaseKey(k), v.OriginalDeadline)\n\t\tc.routerRestoreRouting(v, now)')
    text = text.replace('primaryCtx, primaryCancel := context.WithTimeout(ctx, consts.DefaultDialTimeout)',
                        'primaryCtx, primaryCancel := context.WithTimeout(ctx, routerDNSPrimaryTimeout(ctx, upstream, primaryDialArg))', 1)
    if 'if ctx.Err() != nil { return nil, primaryDialArg, ctx.Err() }' not in text:
        text = text.replace('primaryErr := err', 'primaryErr := err\n\tif ctx.Err() != nil { return nil, primaryDialArg, ctx.Err() }', 1)
    text = text.replace('if !errors.Is(err, ErrDNSTruncated) {', 'if !errors.Is(err, ErrDNSTruncated) && !commonerrors.IsCanceledOrClosed(err) {')
    text = text.replace('func (c *DnsController) logDnsForwardFailure(upstream *dns.Upstream, dialArg *dialArgument, err error) {',
                        'func (c *DnsController) logDnsForwardFailure(upstream *dns.Upstream, dialArg *dialArgument, err error, elapsed time.Duration) {')
    begin = text.index('func (c *DnsController) logDnsForwardFailure(')
    end = text.index('\n}', begin)
    logger = text[begin:end].replace('fields := logrus.Fields{}', 'fields := logrus.Fields{"phase": "dns_forward", "elapsed_ms": elapsed.Milliseconds(), "result": routerDiagnosticResult(err)}')
    text = text[:begin] + logger + text[end:]
    if 'forwardStarted := time.Now()' not in text:
        text = text.replace('respMsg, err := entry.forwarder.ForwardDNS(ctx, data)', 'forwardStarted := time.Now()\n\t\trespMsg, err := entry.forwarder.ForwardDNS(ctx, data)\n\t\trouterLogAttempt(c.log, "dns_forward", 1, forwardStarted, err, nil)')
    text = text.replace('c.logDnsForwardFailure(upstream, dialArg, err)', 'c.logDnsForwardFailure(upstream, dialArg, err, time.Since(forwardStarted))')
    return text


def router_dns_transport(text):
    text = text.replace('func sendHttpDNS(client *http.Client,', 'func sendHttpDNS(ctx context.Context, client *http.Client,')
    text = text.replace('sendHttpDNSFunc(client,', 'sendHttpDNSFunc(ctx, client,')
    text = text.replace('http.NewRequest(http.MethodGet, serverURL.String(), nil)', 'http.NewRequestWithContext(ctx, http.MethodGet, serverURL.String(), nil)')
    text = text.replace('binary.BigEndian.PutUint16(data[0:2], 0)', 'data = append([]byte(nil), data...)\n\tbinary.BigEndian.PutUint16(data[0:2], 0)') if 'data = append([]byte(nil), data...)' not in text else text
    text = text.replace('data = append([]byte(nil), data...)', 'if len(data) < 2 { return nil, fmt.Errorf("invalid DNS request payload: too short") }\n\tdata = append([]byte(nil), data...)') if 'if len(data) < 2 { return nil, fmt.Errorf("invalid DNS request payload: too short") }' not in text else text
    text = text.replace('if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {', 'if ctx.Err() != nil { return nil, ctx.Err() }\n\tif errors.Is(err, net.ErrClosed) {')
    text = text.replace('tlsConn.Handshake()', 'tlsConn.HandshakeContext(ctx)')
    text = text.replace('tls.Client(&netproxy.FakeNetConn{Conn: conn},', 'tls.Client(&routerDNSStreamConn{Conn: &netproxy.FakeNetConn{Conn: conn}},')
    text = text.replace('return &netproxy.FakeNetConn{Conn: conn}, nil', 'return &routerDNSStreamConn{Conn: &netproxy.FakeNetConn{Conn: conn}}, nil')
    text = text.replace('maxConns int\n', 'maxConns int\n\tclosed bool\n\tdone chan struct{}\n\tdialGate chan struct{}\n', 1) if 'dialGate chan struct{}' not in text else text
    text = text.replace('conns:    make([]*pipelinedConn, 0, maxConns),', 'conns:    make([]*pipelinedConn, 0, maxConns),\n\t\tdone: make(chan struct{}),\n\t\tdialGate: make(chan struct{}, 1),') if 'done: make(chan struct{})' not in text else text
    begin, end = text.index('func (p *connPool) get('), text.index('func (p *connPool) pruneClosedLocked(')
    text = text[:begin] + 'func (p *connPool) get(ctx context.Context) (*pipelinedConn, error) { return p.routerGet(ctx) }\n\n' + text[end:]
    begin, end = text.index('func (p *connPool) close('), text.index('// lazyConnPool provides')
    text = text[:begin] + 'func (p *connPool) close() error { return p.routerClose() }\n\n' + text[end:]
    text = text.replace('l.pool.Store(p)\n\t})', 'l.pool.Store(p)\n\t})\n\tif l.closed.Load() {\n\t\tif v := l.pool.Load(); v != nil { _ = v.(*connPool).close() }\n\t\treturn nil\n\t}') if 'if v := l.pool.Load(); v != nil { _ = v.(*connPool).close() }' not in text else text
    text = text.replace('for range 2 {\n\t\tpc, err := d.getPConn(ctx)', 'for range 2 {\n\t\tif err := ctx.Err(); err != nil { return nil, err }\n\t\tpc, err := d.getPConn(ctx)')
    text = text.replace('badConn := false\n\tdefer func() {\n\t\tif !badConn {', 'badConn := false\n\tstopCancel := routerDNSInterruptOnCancel(ctx, conn)\n\tdefer func() {\n\t\tstopCancel()\n\t\tif !badConn && ctx.Err() != nil { udpPool.discard(conn); badConn = true }\n\t\tif !badConn {')
    text = text.replace('_ = pc.conn.Close()', '_ = pc.conn.SetDeadline(time.Now())\n\t\t_ = pc.conn.Close()') if '_ = pc.conn.SetDeadline(time.Now())' not in text else text
    marker = '\t// Allocate ID using bitmap allocator'
    if 'stopCancel := routerDNSInterruptOnCancel(ctx, pc.conn)' not in text:
        text = text.replace(marker, '\tstopCancel := routerDNSInterruptOnCancel(ctx, pc.conn)\n\tdefer stopCancel()\n\n' + marker)
    text = text.replace('pc.writeMu.Unlock()\n\n\tif err != nil {', 'pc.writeMu.Unlock()\n\tif ctx.Err() != nil { pc.Close(); return nil, ctx.Err() }\n\n\tif err != nil {')
    text = replace_go_function(text, 'func (d *DoQ) ForwardDNS(', '\treturn d.routerForwardDNS(ctx, data)')
    for kind in ('DoTCP', 'DoTLS'):
        text = replace_go_function(text, f'func (d *{kind}) ForwardDNS(', '\treturn routerForwardPipeline(ctx, data, d.getPConn)')
    text = text.replace('c, e := quic.DialEarly(ctx, fakePkt, udpAddr, tlsCfg, cfg)\n\t\t\treturn c, e',
                        'return routerQUICDial(ctx, conn, fakePkt, udpAddr, tlsCfg, cfg)')
    text = text.replace('qc, err := quic.DialEarly(ctx, fakePkt, addr, tlsCfg, nil)\n\tif err != nil {\n\t\t_ = conn.Close() // Ensure underlying connection is closed\n\t\treturn nil, err\n\t}\n\treturn qc, nil',
                        'return routerQUICDial(ctx, conn, fakePkt, addr, tlsCfg, nil)')
    text = text.replace('writeMu sync.Mutex', '''routerWrite chan struct{}
    routerMu sync.Mutex
    routerRetiring bool
    routerAbandoned [dnsPipelineMaxIDs]bool
    routerAbandonedCount int''')
    if 'routerWrite: make(chan struct{}, 1)' not in text:
        text = text.replace('idAlloc: newIdBitmap(),', 'idAlloc: newIdBitmap(),\n\t\trouterWrite: make(chan struct{}, 1),')
    text = replace_go_function(text, 'func (pc *pipelinedConn) RoundTrip(', '\treturn pc.routerRoundTrip(ctx, data)')
    begin = text.index('\t\tfor i := range pc.pending {', text.index('func (pc *pipelinedConn) closeWithErr(')) if '\t\tfor i := range pc.pending {' in text else -1
    if begin >= 0:
        end = text.index('\n\t\t}', begin) + len('\n\t\t}')
        text = text[:begin] + '\t\tpc.routerClosePending()' + text[end:]
    begin = text.index('\t\tif respMsg.Id < dnsPipelineMaxIDs {') if '\t\tif respMsg.Id < dnsPipelineMaxIDs {' in text else -1
    if begin >= 0:
        end = text.index('\n\t\t}', begin) + len('\n\t\t}')
        text = text[:begin] + '\t\tpc.routerDeliver(respMsg)' + text[end:]
    return text


def replace_go_function(text, signature, body):
    begin = text.index(signature)
    brace = text.index(' {', begin) + 2
    end = text.index('\n}', brace)
    return text[:brace] + '\n' + body + text[end:]


def router_bootstrap(text):
    text = replace_go_function(text, 'func (c *ControlPlane) resolveIp46WithBootstrapResolvers(',
                               '\treturn c.routerResolveBootstrap(ctx, host, network, race, resolve)')
    return text.replace('\t"github.com/daeuniverse/outbound/protocol/direct"\n', '')


def router_udp_pool_test(text):
    begin = text.index('func TestDoUDPForwardDNSPoolSaturationFailsFast(')
    end = text.index('\nfunc ', begin)
    test = text[begin:end]
    # A context deadline may retire the socket; a real dialer creates a new
    # one. The old fixture returned the same already-closed object on redial.
    test = test.replace('dialCalls.Add(1)\n\t\t\treturn timeoutConn, nil',
                        'if dialCalls.Add(1) == 1 { return timeoutConn, nil }\n\t\t\treturn &deadlineTimeoutUpstreamConn{serverAddr: serverAddr}, nil')
    test = test.replace('if got := dialCalls.Load(); got != 1 {\n\t\tt.Fatalf("dial calls after timed-out reuse = %d, want 1", got)',
                        'if got := dialCalls.Load(); got < 1 || got > 2 {\n\t\tt.Fatalf("dial calls after timeout/recovery = %d, want one reuse or one replacement", got)')
    return text[:begin] + test + text[end:]


def _prepare_in_place(root):
    target = root / "graphql/service/subscription"
    source = target / "mutation_utils.go"
    text = source.read_text(encoding="utf-8")
    manifest = root / ".daed-openwrt-prepare.json"
    previous = json.loads(manifest.read_text()) if manifest.exists() else {}
    digest = lambda value: hashlib.sha256(value).hexdigest()
    marker = "// OpenWrt subscription transport: prepared by openwrt/prepare-wing.py.\n"
    if text.startswith(marker):
        if previous.get("mutation_utils.go") != digest(text.encode()):
            raise ValueError("Prepared wing source was edited; refusing to overwrite it")
    else:
        if hashlib.sha256(text.encode()).hexdigest() != ORIGINAL:
            raise ValueError("Pinned wing subscription source differs; review before applying router additions")
        text = text.replace('"net/http"', '"net/http"\n\t"net/http/cookiejar"\n\t"net/url"\n\t"encoding/json"')
        text = text.replace("func fetchLinks(", "func fetchLinksDirect(", 1)
        text = text.replace("c := http.Client{", "jar, _ := cookiejar.New(nil)\n\tc := http.Client{\n\t\tJar: jar,", 1)
        text = text.replace('if err != nil {\n\t\treturn nil, err\n\t}\n\treq.Header.Set', 'if err != nil {\n\t\treturn nil, fmt.Errorf("invalid subscription URL")\n\t}\n\treq.Header.Set', 1)
        text = text.replace('resp, err = c.Do(req)\n\tif err != nil {\n\t\treturn nil, err\n\t}', '''resp, err = c.Do(req)
	if err != nil {
		// net/http wraps errors with the complete credential-bearing URL.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			if errors.Is(urlError.Err, dae.ErrControlPlaneNotInit) { return nil, dae.ErrControlPlaneNotInit }
			return nil, fmt.Errorf("subscription connection, redirect or TLS verification failed")
		}
		return nil, err
	}
	defer resp.Body.Close()''', 1)
        text = text.replace('return nil, fmt.Errorf("failed to fetch link: %v", resp.Status)', '''// Only identify the known code; never return the provider's free-form body.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var failure struct { Code string `json:"code"` }
		if json.Unmarshal(body, &failure) == nil && failure.Code == "AUTH_FAIL" {
			return nil, fmt.Errorf("subscription HTTP %d AUTH_FAIL: the subscription provider rejected this download exit", resp.StatusCode)
		}
		return nil, fmt.Errorf("subscription HTTP %d", resp.StatusCode)''', 1)
        text = text.replace('\n\tdefer resp.Body.Close()\n\tb, err = io.ReadAll(resp.Body)', '\n\tb, err = io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))', 1)
        text = text.replace('\n\t// Resolve node links.', '\n\tif len(b) > 4<<20 { return nil, fmt.Errorf("subscription response exceeds 4 MiB") }\n\n\t// Resolve node links.', 1)
        text = marker + text
        source.write_text(text, encoding="utf-8", newline="\n")
    # Carry successful-response metadata only for a new import; updates keep names.
    text = text.replace("func fetchLinksDirect(subscriptionLink string)", "func fetchLinksDirect(subscriptionLink string, names ...*string)")
    text = text.replace("timeout/2)", "timeout/2, names...)")
    text = text.replace("transport http.RoundTripper, timeout time.Duration)", "transport http.RoundTripper, timeout time.Duration, names ...*string)")
    begin, end = text.index("func _fetchLinks("), text.index("func Import(")
    fetch = text[begin:end]
    if "detectedSubscriptionName(resp.Header" not in fetch:
        fetch = fetch.replace("\treturn links, nil", "\tif len(names) > 0 && names[0] != nil { *names[0] = detectedSubscriptionName(resp.Header, subscriptionLink) }\n\treturn links, nil", 1)
        text = text[:begin] + fetch + text[end:]
    text = text.replace("links, err := fetchLinks(argument.Link)", "var detectedName string\n\tlinks, err := fetchLinks(argument.Link, &detectedName)")
    if "argument.Tag = &detectedName" not in text:
        text = text.replace("\t/// Create a subscription model.", '\tif argument.Tag == nil && detectedName != "" { argument.Tag = &detectedName }\n\t/// Create a subscription model.')
    begin, end = text.index("func UpdateById("), text.index("\nfunc Remove(")
    text = text[:begin] + '''func UpdateById(ctx context.Context, subId uint) (*db.Subscription, error) {
    return updateSubscriptionSafely(ctx, subId)
}
''' + text[end:]
    text = text.replace('func fetchLinksDirect(subscriptionLink string, names ...*string) (links []string, err error) {', '''func fetchLinksDirect(subscriptionLink string, names ...*string) ([]string, error) {
    return fetchLinksDirectContext(context.Background(), subscriptionLink, names...)
}

func fetchLinksDirectContext(ctx context.Context, subscriptionLink string, names ...*string) (links []string, err error) {''')
    text = text.replace('_fetchLinks(subscriptionLink,', '_fetchLinksContext(ctx, subscriptionLink,')
    text = text.replace('func _fetchLinks(subscriptionLink string, transport http.RoundTripper, timeout time.Duration, names ...*string) (links []string, err error) {', '''func _fetchLinks(subscriptionLink string, transport http.RoundTripper, timeout time.Duration, names ...*string) ([]string, error) {
    return _fetchLinksContext(context.Background(), subscriptionLink, transport, timeout, names...)
}

func _fetchLinksContext(ctx context.Context, subscriptionLink string, transport http.RoundTripper, timeout time.Duration, names ...*string) (links []string, err error) {''')
    text = text.replace('http.NewRequest("GET", subscriptionLink, nil)', 'http.NewRequestWithContext(ctx, "GET", subscriptionLink, nil)')
    text = text.replace('// Retry with dae routing.', 'if ctx.Err() != nil { return nil, ctx.Err() }\n\t\t// Retry with dae routing.') if 'if ctx.Err() != nil' not in text else text
    text = text.replace('links, err := fetchLinks(argument.Link, &detectedName)', 'links, err := fetchLinksContext(c.Statement.Context, argument.Link, &detectedName)')
    # A scheduler must read the committed new row; changing it before commit
    # reads old cron settings and can replace a healthy job on a failed save.
    begin, end = text.index('func Remove('), text.index('func Tag(')
    removal = text[begin:end].replace(TRANSACTION_DEFER, 'defer tx.Rollback()')
    if 'if err = tx.Commit().Error' not in removal:
        removal = removal.replace('\tfor _, id := range ids {', '\tif err = tx.Commit().Error; err != nil { return 0, err }\n\n\tfor _, id := range ids {')
    # A previously prepared copy may already use the shared transaction defer.
    removal = removal.replace('defer db.FinishTransaction(tx, &err)', 'defer tx.Rollback()')
    text = text[:begin] + removal + text[end:]
    begin = text.index('func UpdateCron(')
    cron = text[begin:]
    cron = cron.replace(TRANSACTION_DEFER, 'defer tx.Rollback()')
    if 'schedulerCtx, cancel := context.WithTimeout' not in cron:
        cron = cron.replace('// Update scheduler\n', '// Update scheduler only after a successful commit.\n\tif err = tx.Commit().Error; err != nil { return nil, err }\n\tschedulerCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)\n\tdefer cancel()\n')
        cron = cron.replace('AddUpdateScheduler(ctx, id)', 'AddUpdateScheduler(schedulerCtx, id)')
    text = router_transactions(text[:begin]) + cron
    source.write_text(text, encoding="utf-8", newline="\n")
    generated = {"mutation_utils.go": digest(text.encode())}
    for name in ("subscription_transport.go", "subscription_transport_test.go", "subscription_name.go", "subscription_name_test.go", "subscription_update.go", "subscription_update_test.go"):
        destination = target / name
        data = (HERE / "wing" / name).read_bytes()
        if destination.exists() and destination.read_bytes() != data and previous.get(name) != digest(destination.read_bytes()):
            raise ValueError("Refusing to overwrite different backend addition: " + name)
        shutil.copyfile(HERE / "wing" / name, destination)
        generated[name] = digest(data)
    # Keep latency fixes in the release source, without changing submodule pins.
    patches = {
        "webrender/webrender.go": ("24edc6844f7683ef8cfc6f3e9f8a4345cd9527c9725cbfe4b0f0898a455f5dc9", lambda value: value.replace('statigz.FileServer(webFS.', 'routerFileServer(webFS.', 1)),
        "graphql/service/config/mutation_utils.go": ("f57a5aa16fec110ff2906046aed26897d7ca22308200a45d85394602ab8113c3", router_transactions),
        "graphql/service/dns/mutation_utils.go": ("229f064ba7f4004f1bbdf2c0494f0cffc7cfd57c4c692c0e0f00b704351a1e89", router_transactions),
        "graphql/service/routing/mutation_utils.go": ("651bbbccbf1288da50dfffce15f70ae02434ac1b84be423574f54a382798c2a7", router_transactions),
        "graphql/service/group/mutation_utils.go": ("e4cd1987cdeb9b0bfbb93c17c51c0d38fbbb5ba80fe8e18cc028edebdc0a9120", router_transactions),
        "graphql/service/node/mutation_utils.go": ("0f0fcfb9b102f72b907be4981700db4379d6d4ed5f610d0864ab49e8ace2c5fd", router_transactions),
        "cmd/run.go": ("40511c13085d052870c5688199287ee5ae8285e8a61e6be628cb7fe6315d8566", router_auth),
        "dae/run.go": ("748a95abac2a3dda0741da10d8278cd1dd8e792061d3a0f02f0fab08dcb6e233", router_lifecycle),
        "dae/httpclient.go": ("ea4b8ec3cabf79d8d08c74931c7ed8877df7eae8da4855b73ccc2cf86f2a86fd", lambda value: (HERE / "wing/httpclient.go").read_text(encoding="utf-8")),
        "dae-core/component/interface_manager.go": ("c8a97c9851c2c21d7af3b48d1c2fc514a80e6c838dce25b9ef15888e02752d2c", lambda value: (HERE / "wing/interface_manager.go").read_text(encoding="utf-8")),
        "dae-core/control/dial.go": ("4e9948fde01eed541c5a828a83cdae8f74278ed55d95958e145bd1f113964bfe", router_control_dial),
        "dae-core/control/dns_control.go": ("5f57ad79f164b45f226e0acc130f2b64c1ec4f0eaae90bef6368810339dd8fb8", router_dns_recovery),
        "dae-core/control/dns.go": ("c1e0bc78f71bff6ef4f15ea02f9a078a3f1d7776537578155055999a9b2b28d2", router_dns_transport),
        "dae-core/control/control_plane.go": ("8b57c9d9635aa6c764319a2263421263a52f4dfde8ac398b1d47929b71e53e76", router_bootstrap),
        "dae-core/control/dns_lifecycle_test.go": ("075d244882bb623dce5e37fae748594ee602611ffa2e2697c51d49f46026f44d", lambda value: value.replace('func(client *http.Client,', 'func(ctx context.Context, client *http.Client,')),
        "dae-core/control/dns_udp_packetconn_test.go": ("ac72b165c59e24795c4581740c5a4657c9ef7c305ef361a378124ca96517eba8", router_udp_pool_test),
        "dae-core/component/outbound/dialer/dialer.go": ("c6615ddf375371b1974c6d35ef8660aea8ee907fe88ebe09207560b5da5d39ad", lambda value: value.replace('return &netproxy.FakeNetConn{\n\t\t\t\t\tConn:  conn,\n\t\t\t\t\tLAddr: nil,\n\t\t\t\t\tRAddr: nil,\n\t\t\t\t}, nil', 'return routerCheckConn(conn), nil', 1)),
        "dae-core/component/outbound/dialer/register.go": ("59aa07ff1a3bf7c7dd38353091c3e9cdb2510f256e02e327c1fc768ef4e3ca99", lambda value: value.replace('normalizedLink := normalizeShadowTLSPluginOptions(link)\n\tbaseDialer := newDefaultNetworkDialer(direct.SymmetricDirect, gOption.SoMarkFromDae, gOption.Mptcp)', 'normalizedLink, baseDialer, err := routerProtocolOptions(normalizeShadowTLSPluginOptions(link), newDefaultNetworkDialer(direct.SymmetricDirect, gOption.SoMarkFromDae, gOption.Mptcp), gOption.UDPHopInterval)\n\tif err != nil { return nil, err }', 1)),
        "dae-core/common/subscription/subscription.go": ("c48c3a14937aac13fde5d10af24e6d5774c6b5d2f09f8f23872201c06dcdd05f", lambda value: value.replace('url.Values{"plugin": []string{server.PluginOpts}}.Encode()', 'routerSIP008Plugin(server.Plugin, server.PluginOpts)', 1)),
        "dae/runtime.go": ("582676a55373d44220dced8552f094482d7d35a423d13de98ae0865cbe687062", lambda value: (HERE / "wing/runtime.go").read_text(encoding="utf-8")),
        "graphql/service/node/latency_mutation_utils.go": ("b7e722f4c24dd9606fceaf310daf285b30b8d878413ca20702970ef3b4ca805d", lambda value: (HERE / "wing/node_latency.go").read_text(encoding="utf-8")),
        "graphql/service/node/latency_cache.go": ("4d4fd03249c617bcc9fccb508dbc2d567fa74557dd5900ee046cbf307ff64831", lambda value: value.replace('if err := refreshLatencyCacheIfNeeded(ctx); err != nil {\n\t\treturn nil, err\n\t}', '// Reading results must never start network probes.').replace('if _, ok := merged[id]; !ok {', 'if cached, ok := merged[id]; !ok || resolver.TestedAtV.After(cached.TestedAtV) {')),
        "graphql/mutation.go": ("90a8f23ac625f2d3adf79f7f236d40b6529e2857028cc5ed02f02c0e111ee8ef", lambda value: value.replace('TestNodeLatencies(args *struct', 'TestNodeLatencies(ctx context.Context, args *struct').replace('node.TestLatencies(context.TODO(), args.IDs)', 'node.TestLatencies(ctx, args.IDs)')),
        "graphql/root_schema.go": ("e9cf4836080bf087a0ddba58ed9596abc6e020e437c828d5f718932906b3bef0", lambda value: value.replace('type Mutation {', '''type NodeUdpTest {
    id: ID!
    latencyMs: Int
    alive: Boolean!
    testedAt: Time!
    message: String
    target: String!
}
type Mutation {
    testNodeUDP(id: ID!): NodeUdpTest! @hasRole(role: ADMIN)
    activateGeodata(target: String!, previous: String!): Int! @hasRole(role: ADMIN)''', 1)),
    }
    for relative, (original, transform) in patches.items():
        destination = root / relative
        data = destination.read_bytes()
        if digest(data) == original:
            data = transform(data.decode()).encode()
        elif previous.get(relative) != digest(data):
            raise ValueError("Unknown backend source; refusing to overwrite: " + relative)
        elif relative in ("graphql/service/node/latency_mutation_utils.go", "dae/runtime.go", "dae/httpclient.go", "dae-core/component/interface_manager.go"):
            replacement = "node_latency.go" if relative.endswith("latency_mutation_utils.go") else relative.split("/")[-1]
            data = (HERE / "wing" / replacement).read_bytes()
        elif relative in ("cmd/run.go", "dae/run.go") or transform in (router_transactions, router_control_dial, router_dns_recovery, router_dns_transport, router_bootstrap, router_udp_pool_test):
            data = transform(data.decode()).encode()
        destination.write_bytes(data)
        generated[relative] = digest(data)
    register_file = root / "dae-core/component/outbound/dialer/register.go"
    register_text = register_file.read_text(encoding="utf-8")
    register_text = register_text.replace('daeDialer := NewDialerContext(ctx, d, gOption, iOption, &p)', 'daeDialer := NewDialerContext(ctx, routerProtocolDialer(normalizedLink, d), gOption, iOption, &p)')
    register_file.write_text(register_text, encoding="utf-8", newline="\n")
    generated["dae-core/component/outbound/dialer/register.go"] = digest(register_text.encode())
    mutation_file = root / "graphql/mutation.go"
    mutation_text = mutation_file.read_text(encoding="utf-8")
    mutation_text = mutation_text.replace('ImportSubscription(args *struct', 'ImportSubscription(ctx context.Context, args *struct')
    begin, end = mutation_text.index('func (r *MutationResolver) ImportSubscription('), mutation_text.index('func (r *MutationResolver) UpdateSubscriptionLink(')
    subscription_mutations = mutation_text[begin:end].replace('db.BeginTx(context.TODO())', 'db.BeginTx(ctx)')
    subscription_mutations = subscription_mutations.replace('\ttx.Commit()', '\tif err := tx.Commit().Error; err != nil { return nil, err }')
    subscription_mutations = subscription_mutations.replace('ctx, cancel := context.WithTimeout(', 'schedulerCtx, cancel := context.WithTimeout(').replace('subscription.UpdateAll(ctx)', 'subscription.UpdateAll(schedulerCtx)')
    subscription_mutations = subscription_mutations.replace('UpdateSubscription(args *struct', 'UpdateSubscription(ctx context.Context, args *struct').replace('subscription.Update(context.TODO(), args.ID)', 'subscription.Update(ctx, args.ID)')
    mutation_text = mutation_text[:begin] + subscription_mutations + mutation_text[end:]
    mutation_text = mutation_text.replace(TRANSACTION_DEFER, 'defer db.FinishTransaction(tx, &err)')
    mutation_text = mutation_text.replace('\ttx.Commit()\n\treturn result, nil', '\tif err := tx.Commit().Error; err != nil { return nil, err }\n\treturn result, nil')
    begin, end = mutation_text.index('func (r *MutationResolver) Run('), mutation_text.index('func (r *MutationResolver) CreateDns(')
    run = mutation_text[begin:end]
    run = run.replace('tx := db.BeginTx(context.TODO())\n', 'tx := db.BeginTx(context.TODO())\n\tif tx.Error != nil { return 0, tx.Error }\n\tdefer tx.Rollback()\n') if 'defer tx.Rollback()' not in run else run
    run = run.replace('\ttx.Commit()\n', '\tif err := tx.Commit().Error; err != nil { return 0, err }\n')
    mutation_text = mutation_text[:begin] + run + mutation_text[end:]
    schema_file = root / "graphql/root_schema.go"
    schema_text = schema_file.read_text(encoding="utf-8")
    if 'activateGeodata(target:' not in schema_text:
        schema_text = schema_text.replace('type Mutation {', 'type Mutation {\n    activateGeodata(target: String!, previous: String!): Int! @hasRole(role: ADMIN)', 1)
    schema_file.write_text(schema_text, encoding="utf-8", newline="\n")
    generated["graphql/root_schema.go"] = digest(schema_text.encode())
    mutation_file.write_text(mutation_text, encoding="utf-8", newline="\n")
    generated["graphql/mutation.go"] = digest(mutation_text.encode())
    destination = root / "graphql/service/node/router_latency_test.go"
    key = "graphql/service/node/router_latency_test.go"
    data = (HERE / "wing/node_latency_test.go").read_bytes()
    if destination.exists() and destination.read_bytes() != data and previous.get(key) != digest(destination.read_bytes()):
        raise ValueError("Unknown latency tests; refusing to overwrite")
    destination.write_bytes(data)
    generated[key] = digest(data)
    for relative, source in (
        ("webrender/router_assets.go", "web_assets.go"),
        ("webrender/router_assets_test.go", "web_assets_test.go"),
        ("db/router_transaction.go", "transaction.go"),
        ("graphql/router_transactions_test.go", "transaction_test.go"),
        ("cmd/router_http_context_test.go", "http_context_test.go"),
        ("dae/router_network_ready.go", "network_ready.go"),
        ("dae/router_geodata_update.go", "geodata_update.go"),
        ("dae/router_geodata_update_test.go", "geodata_update_test.go"),
        ("graphql/router_geodata_mutation.go", "geodata_mutation.go"),
        ("graphql/router_geodata_mutation_test.go", "geodata_mutation_test.go"),
        ("dae/router_restart_state.go", "restart_state.go"),
        ("dae/router_restart_state_test.go", "restart_state_test.go"),
        ("cmd/router_readiness_http.go", "readiness_http.go"),
        ("cmd/router_readiness_http_test.go", "readiness_http_test.go"),
        ("dae-core/control/router_restart_core.go", "restart_core.go"),
        ("dae-core/control/router_restart_core_test.go", "restart_core_test.go"),
        ("dae/router_network_ready_test.go", "network_ready_test.go"),
        ("dae/router_httpclient_test.go", "httpclient_test.go"),
        ("graphql/service/node/router_latency_cache.go", "node_latency_cache.go"),
        ("graphql/service/node/router_latency_cache_test.go", "node_latency_cache_test.go"),
        ("graphql/service/node/router_latency_option_test.go", "node_latency_option_test.go"),
        ("graphql/service/node/router_udp.go", "node_udp.go"),
        ("graphql/service/node/router_udp_test.go", "node_udp_test.go"),
        ("graphql/router_udp.go", "udp_mutation.go"),
        ("graphql/router_udp_test.go", "udp_mutation_test.go"),
        ("dae-core/component/outbound/dialer/router_protocol_compat.go", "protocol_compat.go"),
        ("dae-core/component/outbound/dialer/router_protocol_compat_test.go", "protocol_compat_test.go"),
        ("dae-core/component/router_interface_manager_test.go", "interface_manager_test.go"),
        ("dae-core/common/subscription/router_sip008.go", "sip008.go"),
        ("dae-core/common/subscription/router_sip008_test.go", "sip008_test.go"),
        ("graphql/service/node/router_protocol_interop_test.go", "protocol_interop_test.go"),
    ):
        destination = root / relative
        data = (HERE / "wing" / source).read_bytes()
        if destination.exists() and destination.read_bytes() != data and previous.get(relative) != digest(destination.read_bytes()):
            raise ValueError("Unknown protocol compatibility source: " + relative)
        destination.write_bytes(data)
        generated[relative] = digest(data)
    cache_file = root / "graphql/service/node/latency_cache.go"
    cache_text = cache_file.read_text(encoding="utf-8")
    cache_text = cache_text.replace('// Reading results must never start network probes.', '// Reading results must never start network probes.\n\tbefore := latencyCacheEntriesBeforeQuery(ids)') if 'before := latencyCacheEntriesBeforeQuery(ids)' not in cache_text else cache_text
    cache_text = cache_text.replace('merged := snapshotCachedLatencyResults()', 'merged := latencyCacheForNodes(nodes, ids == nil, before)')
    cache_file.write_text(cache_text, encoding="utf-8", newline="\n")
    generated["graphql/service/node/latency_cache.go"] = digest(cache_text.encode())
    # Shared recovery helpers apply to both real traffic and routed API dials.
    for relative, source in (
        ("dae-core/control/router_connection.go", "connection_recovery.go"),
        ("dae-core/control/router_connection_test.go", "connection_recovery_test.go"),
        ("dae-core/control/router_dns_transport.go", "dns_transport.go"),
        ("dae-core/control/router_dns_transport_test.go", "dns_transport_test.go"),
        ("dae-core/control/router_dns_stability.go", "dns_stability.go"),
        ("dae-core/control/router_dns_stability_test.go", "dns_stability_test.go"),
    ):
        destination = root / relative
        data = (HERE / "wing" / source).read_bytes()
        if destination.exists() and destination.read_bytes() != data and previous.get(relative) != digest(destination.read_bytes()):
            raise ValueError("Unknown connection recovery source: " + relative)
        destination.write_bytes(data)
        generated[relative] = digest(data)
    # Tagged socket fixtures validate the real per-plane recorders and adapter.
    # They are not compiled into release binaries and never initialize eBPF.
    for relative, source in (
        ("dae/router_runtime_test.go", "runtime_test.go"),
        ("dae-core/control/router_runtime_fixture.go", "runtime_stats_fixture.go"),
    ):
        destination = root / relative
        data = (HERE / "wing" / source).read_bytes()
        if destination.exists() and destination.read_bytes() != data and previous.get(relative) != digest(destination.read_bytes()):
            raise ValueError("Unknown runtime test fixture; refusing to overwrite: " + relative)
        destination.write_bytes(data)
        generated[relative] = digest(data)
    manifest.write_text(json.dumps(generated, indent=2) + "\n", encoding="utf-8", newline="\n")


def prepare(root):
    # Validate/transform the small reviewed input set in isolation first. An
    # unknown edit in a late file must not leave earlier sources half updated.
    inputs = (
        "webrender/webrender.go",
        "cmd/run.go", "dae/run.go", "dae/httpclient.go", "dae/runtime.go",
        "graphql/mutation.go", "graphql/root_schema.go", "graphql/service/config/mutation_utils.go",
        "graphql/service/dns/mutation_utils.go", "graphql/service/routing/mutation_utils.go",
        "graphql/service/group/mutation_utils.go", "graphql/service/node/mutation_utils.go",
        "graphql/service/node/latency_mutation_utils.go", "graphql/service/node/latency_cache.go",
        "graphql/service/subscription/mutation_utils.go",
        "dae-core/component/outbound/dialer/dialer.go", "dae-core/component/outbound/dialer/register.go",
        "dae-core/component/interface_manager.go",
        "dae-core/control/dial.go", "dae-core/control/dns_control.go",
        "dae-core/control/dns.go", "dae-core/control/dns_lifecycle_test.go", "dae-core/control/control_plane.go",
        "dae-core/control/dns_udp_packetconn_test.go",
        "dae-core/common/subscription/subscription.go",
    )
    manifest = root / ".daed-openwrt-prepare.json"
    previous = json.loads(manifest.read_text()) if manifest.exists() else {}
    def relative(key):
        return key if "/" in key else "graphql/service/subscription/" + key
    with tempfile.TemporaryDirectory(prefix=".nailink-prepare-", dir=root.parent) as temporary:
        stage = Path(temporary)
        staged = set(inputs) | {relative(key) for key in previous}
        for name in staged:
            source = root / name
            if source.is_file():
                destination = stage / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source, destination)
        for directory in ("db", "dae-core/control"):
            (stage / directory).mkdir(parents=True, exist_ok=True)
        if manifest.exists():
            shutil.copy2(manifest, stage / manifest.name)
        _prepare_in_place(stage)
        generated = json.loads((stage / manifest.name).read_text())
        for key in generated:
            name = relative(key)
            destination = root / name
            if name not in staged and destination.exists() and destination.read_bytes() != (stage / name).read_bytes():
                raise ValueError("Unknown existing backend addition; refusing to overwrite: " + name)
        for key in generated:
            name = relative(key)
            destination = root / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            os.replace(stage / name, destination)
        os.replace(stage / manifest.name, manifest)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("wing", type=Path)
    args = parser.parse_args()
    prepare(args.wing.resolve(strict=True))
    print("Router backend additions prepared; pinned submodule revisions unchanged")
