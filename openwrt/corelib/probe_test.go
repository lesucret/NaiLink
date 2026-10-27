// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import "testing"

func TestProbeProtocolFailuresReturnOnlyFixedCodes(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"[Ws]: private://secret@host/path: bad status: 504", "gateway_failed"},
		{"websocket: bad handshake: HTTP 502 private-token", "gateway_failed"},
		{"transport: unexpected HTTP response status: 504 private-token", "gateway_failed"},
		{"transport: unexpected HTTP response: 503 private-token", "gateway_failed"},
		{"websocket: bad handshake: HTTP 403 private-token", "websocket_failed"},
		{"tls handshake timeout: private-host", "tls_handshake_timeout"},
		{"dial tcp private-host:443: i/o timeout", "node_tcp_timeout"},
		{"HTTP 504 from a check target", ""},
		{"private-token: unknown error", ""},
	} {
		if got := ProbeProtocolFailure(tc.input); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
