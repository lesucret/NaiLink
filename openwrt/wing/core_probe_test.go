// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"errors"
	"github.com/sirupsen/logrus"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestRouterCoreProbeFailureDoesNotHideTimeoutOrLeakValues(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{&url.Error{Op: "HEAD", URL: "https://secret.invalid", Err: context.DeadlineExceeded}, "timeout"},
		{context.Canceled, "cancelled"},
		{io.ErrUnexpectedEOF, "connection_closed"},
		{errors.New("private-password server-controlled failure"), "unreachable"},
	} {
		if got := routerCoreProbeFailure(c.err); got != c.want {
			t.Fatal(got, c.want)
		}
	}
}

func TestRouterCoreProbeNativeErrorsAreScopedAndRedacted(t *testing.T) {
	p := &routerProbeErrors{codes: map[string]string{}}
	cases := []struct{ message, code string }{
		{"dial tcp private.example:443: i/o timeout", "timeout"},
		{"tls: failed to verify certificate: private-value", "tls_failed"},
		{"REALITY verification failed private-value", "authentication_failed"},
		{"lookup private.example: no such host", "dns_failed"},
		{"dial tcp private.example: connection refused", "connection_refused"},
		{"read private.example: connection reset by peer", "connection_closed"},
	}
	var wg sync.WaitGroup
	for i, c := range cases {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "probe-" + string(rune('0'+i))
			_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:" + name + "]: " + c.message})
		}()
	}
	wg.Wait()
	for i, c := range cases {
		if got := p.message("probe-"+string(rune('0'+i)), "unreachable"); got != c.code || strings.Contains(got, "private") {
			t.Fatal(got, c.code)
		}
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:real-user-label]: certificate failed"})
	if got := p.message("real-user-label", "unreachable"); got != "unreachable" {
		t.Fatal("accepted a non-probe log")
	}
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: authentication failed"})
	_ = p.Fire(&logrus.Entry{Message: "outbound/test[node:probe-0]: i/o timeout"})
	if got := p.message("probe-0", "unreachable"); got != "authentication_failed" {
		t.Fatal("lost actionable native error", got)
	}
}
