// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
)

func testSingBoxNodes(ctx context.Context, nodes []db.Node) ([]*LatencyResolver, error) {
	var model db.Config
	if err := db.DB(ctx).Where("selected = ?", true).First(&model).Error; err != nil {
		return nil, err
	}
	parsed, err := dae.ParseConfig(&model.Global, nil, nil)
	if err != nil {
		return nil, err
	}
	// Resolve once outside the measured request, just as dae manual tests do.
	// Both cores receive the same configured URL, method and available families.
	option, err := latencyProbeOption(ctx)
	if err != nil {
		return nil, err
	}
	parsed.Global.TcpCheckUrl = option.TcpCheckOptionRaw.Raw
	parsed.Global.TcpCheckHttpMethod = option.TcpCheckOptionRaw.Method
	links := make([]string, len(nodes))
	for i, n := range nodes {
		links[i] = n.Link
	}
	results, err := dae.ProbeRouterCoreNodes(ctx, links, parsed.Global)
	if err != nil {
		return nil, err
	}
	values := make([]*LatencyResolver, len(nodes))
	for i, result := range results {
		r := &LatencyResolver{NodeID: nodes[i].ID, AliveVal: result.Alive, TestedAtV: result.Tested, NodeFingerprintV: latencyNodeFingerprint(&nodes[i])}
		if result.Alive {
			ms := result.Latency
			r.LatencyMsV = &ms
		} else {
			message := result.Message
			r.MessageV = &message
		}
		values[i] = r
	}
	if dae.RouterCoreName() == "sing-box" {
		storeLatencyResults(values)
	}
	return values, nil
}
