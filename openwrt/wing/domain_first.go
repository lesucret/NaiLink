// SPDX-License-Identifier: AGPL-3.0-only
package control

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/daeuniverse/dae/common"
	"github.com/daeuniverse/dae/common/consts"
	"github.com/sirupsen/logrus"
)

const routerFirstDomainBudget = 250 * time.Millisecond

var routerFirstDomainWaiters = make(chan struct{}, 64)
var routerFirstDomainProbes = make(chan struct{}, 16)

// A client can retain an IP learned before this core started. In domain mode,
// the first sniffed name used to warm its validation asynchronously but dial the
// stale IP immediately. Let the existing, deduplicated probe finish briefly so
// the FIRST connection can also use a verified domain. Never trust raw SNI or
// alter direct routes, IP mode, bootstrap resolvers or negative-cache policy.
func (c *ControlPlane) routerChooseDialTarget(ctx context.Context, outbound consts.OutboundIndex, dst netip.AddrPort, domain string) (string, bool, bool) {
	if ctx == nil {
		ctx = c.ctx
	}
	if ctx.Err() != nil {
		return dst.String(), false, true
	}
	if outbound.IsReserved() || c.dialMode != consts.DialMode_Domain || domain == "" || isIPLikeDomain(domain) {
		return c.ChooseDialTarget(outbound, dst, domain)
	}
	if known, _ := c.lookupRealDomainCache(domain); known || c.dnsController.HasDnsKnowledge(c.dnsController.cacheKey(domain, common.AddrToDnsType(dst.Addr()))) {
		return c.ChooseDialTarget(outbound, dst, domain)
	}
	select {
	case routerFirstDomainWaiters <- struct{}{}:
		defer func() { <-routerFirstDomainWaiters }()
	default:
		return dst.String(), false, true
	}
	result := c.realDomainProbeS.DoChan(domain, func() (any, error) {
		select {
		case routerFirstDomainProbes <- struct{}{}:
			defer func() { <-routerFirstDomainProbes }()
		default:
			return false, nil
		}
		return c.probeAndUpdateRealDomain(domain), nil
	})
	started := time.Now()
	decision := "unverified"
	probeErr := errors.New("domain_unverified")
	defer func() {
		c.routerLogAttempt("first_domain_validation", 1, started, probeErr, logrus.Fields{"validation": decision})
	}()
	timer := time.NewTimer(routerFirstDomainBudget)
	defer timer.Stop()
	select {
	case response := <-result:
		if real, ok := response.Val.(bool); ok && real && response.Err == nil && ctx.Err() == nil && c.ctx.Err() == nil && time.Since(started) < routerFirstDomainBudget {
			decision, probeErr = "verified", nil
			return c.ChooseDialTarget(outbound, dst, domain)
		}
	case <-timer.C:
		decision, probeErr = "budget_expired", context.DeadlineExceeded
	case <-ctx.Done():
		decision, probeErr = "cancelled", ctx.Err()
	case <-c.ctx.Done():
		decision, probeErr = "retired", c.ctx.Err()
	}
	return dst.String(), false, true
}
