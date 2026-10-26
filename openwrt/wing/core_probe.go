// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	dnsmessage "github.com/miekg/dns"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type RouterCoreProbe struct {
	Alive   bool
	Latency int32
	Message string
	Tested  time.Time
}

// Use the selected core's SOCKS UDP relay rather than a dae dialer when the
// active core is sing-box. This process has no TUN, routes or user data writes.
func ProbeRouterCoreUDP(ctx context.Context, link string, global daeConfig.Global) (RouterCoreProbe, error) {
	result := RouterCoreProbe{Tested: time.Now()}
	select {
	case routerCoreProbeSlots <- struct{}{}:
		defer func() { <-routerCoreProbeSlots }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	conf, names, results, err := routerProbeConfiguration([]string{link}, global)
	if err != nil {
		return result, err
	}
	if len(names) == 0 {
		return results[0], nil
	}
	conf.Routing.Fallback = "probe-0"
	session, err := routerStartProbe(ctx, conf)
	if err != nil {
		return result, err
	}
	defer routerStopProbe(session)
	if len(global.UdpCheckDns) == 0 {
		return result, errors.New("check_target_failed")
	}
	host, port, err := net.SplitHostPort(global.UdpCheckDns[0])
	if err != nil {
		return result, errors.New("check_target_failed")
	}
	if len(global.UdpCheckDns) > 1 {
		host = strings.Trim(global.UdpCheckDns[1], "[]")
	}
	ip := net.ParseIP(host)
	p, e := strconv.Atoi(port)
	if e != nil || ip == nil || p < 1 || p > 65535 {
		return result, errors.New("check_target_failed")
	}
	start := time.Now()
	err = routerSOCKSUDPQuery(ctx, session, ip, p)
	result.Tested = time.Now()
	if err == nil {
		result.Alive = true
		result.Latency = int32(time.Since(start).Milliseconds())
	} else {
		result.Message = "udp_test_failed"
		if ctx.Err() != nil {
			result.Message = "cancelled"
		}
	}
	return result, nil
}

func routerSOCKSUDPQuery(ctx context.Context, session *routerSingBoxSession, target net.IP, port int) error {
	tcp, err := (&net.Dialer{}).DialContext(ctx, "tcp4", routercore.SocksAddress(session.ports.Mixed))
	if err != nil {
		return err
	}
	defer tcp.Close()
	stop := context.AfterFunc(ctx, func() { _ = tcp.Close() })
	defer stop()
	deadline := time.Now().Add(6 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = tcp.SetDeadline(deadline)
	if _, err = tcp.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var response [2]byte
	if _, err = io.ReadFull(tcp, response[:]); err != nil || response != [2]byte{5, 2} {
		return errors.New("socks_auth_failed")
	}
	user, password := "nailink", session.ports.Secret
	auth := append([]byte{1, byte(len(user))}, []byte(user)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	if _, err = tcp.Write(auth); err != nil {
		return err
	}
	if _, err = io.ReadFull(tcp, response[:]); err != nil || response != [2]byte{1, 0} {
		return errors.New("socks_auth_failed")
	}
	if _, err = tcp.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return err
	}
	var header [4]byte
	if _, err = io.ReadFull(tcp, header[:]); err != nil || header[0] != 5 || header[1] != 0 {
		return errors.New("udp_unsupported")
	}
	length := 4
	if header[3] == 4 {
		length = 16
	} else if header[3] != 1 {
		return errors.New("invalid_udp_relay")
	}
	address := make([]byte, length+2)
	if _, err = io.ReadFull(tcp, address); err != nil {
		return err
	}
	relay := &net.UDPAddr{IP: net.IP(address[:length]), Port: int(binary.BigEndian.Uint16(address[length:]))}
	if !relay.IP.IsLoopback() && !relay.IP.IsUnspecified() {
		return errors.New("invalid_udp_relay")
	}
	relay.IP = net.IPv4(127, 0, 0, 1)
	udp, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		return err
	}
	defer udp.Close()
	stopUDP := context.AfterFunc(ctx, func() { _ = udp.Close() })
	defer stopUDP()
	_ = udp.SetDeadline(deadline)
	query := new(dnsmessage.Msg)
	query.SetQuestion("www.google.com.", dnsmessage.TypeA)
	data, err := query.Pack()
	if err != nil {
		return err
	}
	packet := []byte{0, 0, 0}
	if ip4 := target.To4(); ip4 != nil {
		packet = append(packet, 1)
		packet = append(packet, ip4...)
	} else {
		packet = append(packet, 4)
		packet = append(packet, target.To16()...)
	}
	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	packet = append(packet, data...)
	if _, err = udp.Write(packet); err != nil {
		return err
	}
	buffer := make([]byte, 8192)
	n, err := udp.Read(buffer)
	if err != nil {
		return err
	}
	if n < 10 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 {
		return errors.New("invalid_udp_reply")
	}
	offset := 10
	if buffer[3] == 4 {
		offset = 22
	} else if buffer[3] == 3 && n > 4 {
		offset = 7 + int(buffer[4])
	} else if buffer[3] != 1 {
		return errors.New("invalid_udp_reply")
	}
	answer := new(dnsmessage.Msg)
	if offset >= n || answer.Unpack(buffer[offset:n]) != nil || answer.Id != query.Id || !answer.Response || answer.Rcode != dnsmessage.RcodeSuccess || len(answer.Answer) == 0 || len(answer.Question) != 1 || answer.Question[0] != query.Question[0] {
		return errors.New("invalid_dns_reply")
	}
	return nil
}

var routerCoreProbeSlots = make(chan struct{}, 2)

func routerProbeConfiguration(links []string, global daeConfig.Global) (*daeConfig.Config, map[int]string, []RouterCoreProbe, error) {
	conf := &daeConfig.Config{Global: global, Routing: daeConfig.Routing{Fallback: "direct"}}
	manager := currentRouterCoreManager()
	if manager == nil {
		return nil, nil, nil, errors.New("core_not_ready")
	}
	manager.mu.RLock()
	running := manager.conf
	manager.mu.RUnlock()
	if running != nil && len(running.Dns.Upstream) > 0 {
		conf.Dns.Upstream = running.Dns.Upstream[:1]
	}
	names := map[int]string{}
	results := make([]RouterCoreProbe, len(links))
	for i, link := range links {
		name := "probe-" + strconv.Itoa(i)
		fingerprint := ""
		if global.TlsImplementation == "utls" {
			fingerprint = global.UtlsImitate
		}
		if _, err := routercore.Node(link, "node:"+name, routercore.NodeOptions{AllowInsecure: global.AllowInsecure, UTLS: fingerprint}); err != nil {
			results[i] = RouterCoreProbe{Message: err.Error(), Tested: time.Now()}
			continue
		}
		names[i] = name
		conf.Node = append(conf.Node, daeConfig.KeyableString(name+":"+link))
		conf.Group = append(conf.Group, daeConfig.Group{Name: name, Policy: "fixed", Filter: [][]*config_parser.Function{{{Name: "name", Params: []*config_parser.Param{{Val: name}}}}}, FilterAnnotation: [][]*config_parser.Param{{}}})
	}
	return conf, names, results, nil
}
func routerStartProbe(ctx context.Context, conf *daeConfig.Config) (*routerSingBoxSession, error) {
	log := logrus.New()
	log.SetOutput(io.Discard)
	session, err := newRouterSingBoxSession(ctx, log, conf, nil, false, func(profile sbObject, ports sbPorts) {
		users := []sbObject{{"username": "nailink", "password": ports.Secret}}
		var rules []sbObject
		for _, group := range conf.Group {
			users = append(users, sbObject{"username": group.Name, "password": ports.Secret})
			rules = append(rules, sbObject{"auth_user": []string{group.Name}, "action": "route", "outbound": "node:" + group.Name})
		}
		profile["inbounds"].([]sbObject)[0]["users"] = users
		route := profile["route"].(sbObject)
		route["rules"] = append(rules, route["rules"].([]sbObject)...)
	})
	if err != nil {
		return nil, err
	}
	if err = session.Ready(ctx); err != nil {
		routerStopProbe(session)
		return nil, err
	}
	return session.(*routerSingBoxSession), nil
}
func routerStopProbe(session routerCoreSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = session.Stop(ctx)
}
func ProbeRouterCoreNodes(ctx context.Context, links []string, global daeConfig.Global) ([]RouterCoreProbe, error) {
	select {
	case routerCoreProbeSlots <- struct{}{}:
		defer func() { <-routerCoreProbeSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	conf, names, results, err := routerProbeConfiguration(links, global)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return results, nil
	}
	if len(global.TcpCheckUrl) == 0 {
		return nil, errors.New("missing_check_url")
	}
	session, err := routerStartProbe(ctx, conf)
	if err != nil {
		return nil, err
	}
	defer routerStopProbe(session)
	var workers sync.WaitGroup
	for index, name := range names {
		index, name := index, name
		workers.Add(1)
		go func() {
			defer workers.Done()
			started := time.Now()
			request, err := http.NewRequestWithContext(ctx, "HEAD", global.TcpCheckUrl[0], nil)
			result := RouterCoreProbe{Tested: time.Now(), Message: "test_failed"}
			if err == nil {
				dialer, dialErr := proxy.SOCKS5("tcp", routercore.SocksAddress(session.ports.Mixed), &proxy.Auth{User: name, Password: session.ports.Secret}, &net.Dialer{Timeout: 8 * time.Second})
				if dialErr != nil {
					results[index] = result
					return
				}
				transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if len(global.TcpCheckUrl) > 1 {
						host, port, e := net.SplitHostPort(address)
						parsed, e2 := url.Parse(global.TcpCheckUrl[0])
						if e == nil && e2 == nil && host == parsed.Hostname() && net.ParseIP(global.TcpCheckUrl[1]) != nil {
							address = net.JoinHostPort(global.TcpCheckUrl[1], port)
						}
					}
					return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
				}}
				defer transport.CloseIdleConnections()
				response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(request)
				if err != nil {
					result.Message = "unreachable"
				}
				if err == nil {
					defer response.Body.Close()
					if response.StatusCode >= 200 && response.StatusCode < 500 {
						result.Alive = true
						result.Latency = int32(time.Since(started).Milliseconds())
						result.Message = ""
					}
				}
			}
			if ctx.Err() != nil {
				result.Message = "cancelled"
			}
			result.Tested = time.Now()
			results[index] = result
		}()
	}
	workers.Wait()
	return results, nil
}
