// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"time"
)

func ReadRouterReadiness(details bool) RouterReadiness {
	m := currentRouterCoreManager()
	if m != nil {
		m.mu.RLock()
		name, phase, conf, active := m.state.Current, m.phase, m.conf, m.active
		m.mu.RUnlock()
		if phase != "ready" {
			return RouterReadiness{Core: name, Phase: "core_" + phase}
		}
		if name == "sing-box" {
			result := RouterReadiness{Core: name, Phase: "starting", Configured: conf != EmptyConfig}
			if s, ok := active.(*routerSingBoxSession); ok {
				s = s.current()
				result.Listening = s.ready.Load()
				result.Ready = result.Listening
				result.Initialized = result.Listening
				result.DNSReady = result.Listening
				result.RoutingReady = result.Listening
				result.RestartDNS = "core_specific_cache"
				if result.Ready {
					result.Phase = "ready"
					if !result.Configured {
						result.Phase = "inactive"
					}
				}
			}
			return result
		}
	}
	result := readRouterDaeReadiness(details)
	result.Core = "dae"
	return result
}
func routerCoreOverview(windowSec, maxPoints int) (*RuntimeOverview, bool, error) {
	m := currentRouterCoreManager()
	if m == nil {
		return nil, false, nil
	}
	m.mu.RLock()
	name, active := m.state.Current, m.active
	m.mu.RUnlock()
	if name != "sing-box" {
		return nil, false, nil
	}
	original, ok := active.(*routerSingBoxSession)
	if !ok {
		return nil, true, errors.New("core_not_ready")
	}
	s := original.current()
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	now := time.Now()
	if s.stats == nil || now.Sub(s.stats.UpdatedAt) >= 500*time.Millisecond {
		var connections struct {
			UploadTotal   uint64 `json:"uploadTotal"`
			DownloadTotal uint64 `json:"downloadTotal"`
			Connections   []struct {
				Metadata struct {
					Network string `json:"network"`
				} `json:"metadata"`
			} `json:"connections"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.api(ctx, "/connections", &connections)
		cancel()
		if err != nil {
			return nil, true, err
		}
		next := &RuntimeOverview{UpdatedAt: now, UploadTotal: connections.UploadTotal, DownloadTotal: connections.DownloadTotal, ActiveConnections: len(connections.Connections)}
		for _, conn := range connections.Connections {
			if conn.Metadata.Network == "udp" {
				next.UDPSessions++
			}
		}
		if previous := s.stats; previous != nil {
			seconds := now.Sub(previous.UpdatedAt).Seconds()
			if next.UploadTotal >= previous.UploadTotal {
				next.UploadRate = uint64(float64(next.UploadTotal-previous.UploadTotal) / seconds)
			}
			if next.DownloadTotal >= previous.DownloadTotal {
				next.DownloadRate = uint64(float64(next.DownloadTotal-previous.DownloadTotal) / seconds)
			}
		}
		s.stats = next
		s.samples = append(s.samples, RuntimeTrafficSample{Timestamp: now, UploadRate: next.UploadRate, DownloadRate: next.DownloadRate})
		first := 0
		for first < len(s.samples) && (now.Sub(s.samples[first].Timestamp) > time.Hour || len(s.samples)-first > 7200) {
			first++
		}
		if first > 0 {
			s.samples = append([]RuntimeTrafficSample(nil), s.samples[first:]...)
		}
	}
	result := *s.stats
	if windowSec < 1 {
		windowSec = 60
	}
	if windowSec > 3600 {
		windowSec = 3600
	}
	if maxPoints < 2 {
		maxPoints = 60
	}
	if maxPoints > 600 {
		maxPoints = 600
	}
	var available []RuntimeTrafficSample
	for _, sample := range s.samples {
		if now.Sub(sample.Timestamp) <= time.Duration(windowSec)*time.Second {
			available = append(available, sample)
		}
	}
	if len(available) <= maxPoints {
		result.Samples = available
	} else {
		for i := 0; i < maxPoints; i++ {
			result.Samples = append(result.Samples, available[i*(len(available)-1)/(maxPoints-1)])
		}
	}
	return &result, true, nil
}
