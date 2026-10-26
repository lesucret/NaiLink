// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/graph-gophers/graphql-go"
)

func TestRouterManualResultIsNotReplacedByNewerBackgroundCheck(t *testing.T) {
	ms := int32(180)
	manual := &LatencyResolver{NodeID: 1, AliveVal: true, LatencyMsV: &ms, TestedAtV: time.Now().Add(-time.Minute)}
	auto := &LatencyResolver{NodeID: 1, AliveVal: true, TestedAtV: time.Now()}
	merged := map[uint]*LatencyResolver{1: manual}
	mergeRuntimeLatencyResults(merged, map[uint]*LatencyResolver{1: auto, 2: {NodeID: 2, TestedAtV: time.Now()}})
	if merged[1] != manual || *merged[1].LatencyMsV != 180 || merged[1].Source() != "manual" {
		t.Fatal("manual fresh-connection result was replaced by background measurement")
	}
	if merged[2].Source() != "automatic" || auto.SourceV != "" {
		t.Fatal("result source missing or shared runtime result mutated")
	}
	manual.AliveVal, manual.LatencyMsV = false, nil
	mergeRuntimeLatencyResults(merged, map[uint]*LatencyResolver{1: auto})
	if merged[1].AliveVal {
		t.Fatal("background result hid an explicit test failure")
	}
}

func TestRouterManualResultDoesNotSurviveNodeConnectionEdits(t *testing.T) {
	nodeLatencyCache.mu.Lock()
	previous := nodeLatencyCache.items
	nodeLatencyCache.items = make(map[uint]*LatencyResolver)
	nodeLatencyCache.mu.Unlock()
	t.Cleanup(func() { nodeLatencyCache.mu.Lock(); nodeLatencyCache.items = previous; nodeLatencyCache.mu.Unlock() })
	old := db.Node{ID: 1, Link: "socks5://127.0.0.1:1111"}
	updated := db.Node{ID: 1, Link: "socks5://127.0.0.1:2222"}
	storeLatencyResults([]*LatencyResolver{{NodeID: 1, NodeFingerprintV: latencyNodeFingerprint(&old)}})
	if len(latencyCacheForNodes([]db.Node{old}, false, nil)) != 1 {
		t.Fatal("matching result lost")
	}
	if len(latencyCacheForNodes([]db.Node{updated}, false, nil)) != 0 {
		t.Fatal("edited node inherited its previous server's result")
	}
	storeLatencyResults([]*LatencyResolver{{NodeID: 1, NodeFingerprintV: latencyNodeFingerprint(&updated)}})
	// Simulate an older DB query completing after a new test. It must not delete
	// that concurrently written measurement, even though it does not return it.
	latencyCacheForNodes([]db.Node{old}, false, nil)
	if len(latencyCacheForNodes([]db.Node{updated}, false, nil)) != 1 {
		t.Fatal("concurrent new result was discarded")
	}
}

func TestRouterLatencyCachePrunesDeletedNodesWithoutDiscardingConcurrentWrites(t *testing.T) {
	nodeLatencyCache.mu.Lock()
	previous := nodeLatencyCache.items
	nodeLatencyCache.items = make(map[uint]*LatencyResolver)
	nodeLatencyCache.mu.Unlock()
	t.Cleanup(func() { nodeLatencyCache.mu.Lock(); nodeLatencyCache.items = previous; nodeLatencyCache.mu.Unlock() })
	storeLatencyResults([]*LatencyResolver{{NodeID: 1}, {NodeID: 2}, {NodeID: 3}})
	before := latencyCacheEntriesBeforeQuery(nil)
	storeLatencyResults([]*LatencyResolver{{NodeID: 3}, {NodeID: 4}})
	results := latencyCacheForNodes([]db.Node{{ID: 1}}, true, before)
	if len(results) != 1 || results[1] == nil {
		t.Fatalf("incorrect query results: %v", results)
	}
	cache := snapshotCachedLatencyResults()
	if cache[2] != nil || cache[3] == nil || cache[4] == nil {
		t.Fatal("deleted results retained or concurrent writes discarded")
	}
	ids := []graphql.ID{}
	if latencyCacheEntriesBeforeQuery(&ids) != nil {
		t.Fatal("scoped query copied the entire cache")
	}
	latencyCacheForNodes(nil, false, nil)
	if len(snapshotCachedLatencyResults()) != 3 {
		t.Fatal("a scoped query pruned unrelated nodes")
	}
	latencyCacheForNodes([]db.Node{{ID: 1}}, true, latencyCacheEntriesBeforeQuery(nil))
	if len(snapshotCachedLatencyResults()) != 1 {
		t.Fatal("subsequent full query did not prune obsolete writes")
	}
}
