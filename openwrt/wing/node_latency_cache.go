// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"crypto/sha256"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
	"time"
)

func latencyNodeFingerprint(node *db.Node) [32]byte {
	return sha256.Sum256([]byte(node.Link))
}

var latencyCacheCore = "dae" // guarded by nodeLatencyCache.mu

func storeCoreLatencyResults(core string, results []*LatencyResolver) bool {
	nodeLatencyCache.mu.Lock()
	defer nodeLatencyCache.mu.Unlock()
	if dae.RouterCoreName() != core {
		return false
	}
	if latencyCacheCore != core {
		nodeLatencyCache.items = map[uint]*LatencyResolver{}
		latencyCacheCore = core
	}
	for _, r := range results {
		if r != nil {
			nodeLatencyCache.items[r.NodeID] = cloneLatencyResolver(r)
		}
	}
	nodeLatencyCache.updatedAt = time.Now()
	return true
}

func mergeRuntimeLatencyResults(manual, automatic map[uint]*LatencyResolver) {
	for id, value := range automatic {
		// Background checks can reuse connections. Never silently replace an
		// explicit fresh-connection measurement with that different metric.
		if manual[id] == nil && value != nil {
			result := cloneLatencyResolver(value)
			result.SourceV = "automatic"
			manual[id] = result
		}
	}
}

func syncLatencyCacheCore() {
	core := dae.RouterCoreName()
	if core != latencyCacheCore {
		nodeLatencyCache.items = map[uint]*LatencyResolver{}
		latencyCacheCore = core
	}
}

func latencyCacheEntriesBeforeQuery(ids *[]graphql.ID) map[uint]*LatencyResolver {
	if ids != nil {
		return nil
	}
	nodeLatencyCache.mu.RLock()
	defer nodeLatencyCache.mu.RUnlock()
	entries := make(map[uint]*LatencyResolver, len(nodeLatencyCache.items))
	for id, value := range nodeLatencyCache.items {
		// Stored entries are immutable and replaced on writes. Retain identity
		// rather than cloning results that will not be returned to the caller.
		entries[id] = value
	}
	return entries
}

func latencyCacheForNodes(nodes []db.Node, all bool, before map[uint]*LatencyResolver) map[uint]*LatencyResolver {
	nodeLatencyCache.mu.Lock()
	defer nodeLatencyCache.mu.Unlock()
	syncLatencyCacheCore()
	results := make(map[uint]*LatencyResolver, len(nodes))
	live := make(map[uint]bool, len(nodes))
	for _, node := range nodes {
		live[node.ID] = true
		if cached := nodeLatencyCache.items[node.ID]; cached != nil {
			if cached.NodeFingerprintV != ([32]byte{}) && cached.NodeFingerprintV != latencyNodeFingerprint(&node) {
				// A node may retain its ID after an edit or subscription refresh.
				// Do not show its previous server's measurement. Do not delete here:
				// a new test can finish after this query's database snapshot.
				continue
			}
			results[node.ID] = cloneLatencyResolver(cached)
		}
	}
	if all {
		for id, old := range before {
			// Do not discard a result written while the database query ran.
			if !live[id] && nodeLatencyCache.items[id] == old {
				delete(nodeLatencyCache.items, id)
			}
		}
	}
	return results
}
