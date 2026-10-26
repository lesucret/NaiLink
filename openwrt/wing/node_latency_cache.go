// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
)

var latencyCacheCore = "dae" // guarded by nodeLatencyCache.mu
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
