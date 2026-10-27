// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"regexp"
	"strings"
	"time"
)

// Manual requests include a fresh proxy and TLS/WS handshake. Allow slow but
// usable lines to finish; automatic health-check policy remains unchanged.
const ManualProbeTimeout = 8 * time.Second

var probeGatewayStatus = regexp.MustCompile(`(?:status(?: code)?|http)[\s:=]+50[234]\b`)

// Classify only recognizable protocol/transport failures. Never retain or
// return the supplied text: it can contain credentials and remote messages.
func ProbeProtocolFailure(message string) string {
	text := strings.ToLower(message)
	ws := strings.Contains(text, "websocket") || strings.Contains(text, "[ws]")
	if ws && probeGatewayStatus.MatchString(text) {
		return "gateway_failed"
	}
	if ws && (strings.Contains(text, "bad handshake") || strings.Contains(text, "bad status") || strings.Contains(text, "unexpected status")) {
		return "websocket_failed"
	}
	if strings.Contains(text, "tls handshake timeout") || strings.Contains(text, "tls handshake: context deadline exceeded") {
		return "tls_handshake_timeout"
	}
	if strings.Contains(text, "dial tcp ") && (strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded")) {
		return "node_tcp_timeout"
	}
	return ""
}
