// SPDX-License-Identifier: AGPL-3.0-only
package node

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/db"
	dialer "github.com/daeuniverse/dae/component/outbound/dialer"
	"github.com/graph-gophers/graphql-go"
	"strings"
	"testing"
	"time"
)

func readJob(t *testing.T, id string) testJobStatus {
	t.Helper()
	raw, err := LatencyJob(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var s testJobStatus
	if json.Unmarshal([]byte(raw), &s) != nil {
		t.Fatal("bad job status")
	}
	return s
}
func waitJob(t *testing.T, id string) testJobStatus {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
		s := readJob(t, id)
		if s.Phase != "running" && s.Phase != "stopping" {
			for {
				latencyJobs.Lock()
				active := latencyJobs.active
				latencyJobs.Unlock()
				if active == nil {
					return s
				}
				time.Sleep(time.Millisecond)
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job failed to finish")
	return testJobStatus{}
}
func jobFixture(t *testing.T) []graphql.ID {
	t.Helper()
	latencyOptionFixture(t)
	var ids []graphql.ID
	for range 4 {
		n := db.Node{Link: "invalid://private-fixture-password", Name: "fixture"}
		if db.DB(context.Background()).Create(&n).Error != nil {
			t.Fatal("fixture insert failed")
		}
		ids = append(ids, common.EncodeCursor(n.ID))
	}
	return ids
}
func TestLatencyJobBoundAndNoImplicitProbes(t *testing.T) {
	for _, ids := range [][]graphql.ID{nil, make([]graphql.ID, 1001)} {
		if _, err := StartLatencyJob(context.Background(), ids, strings.Repeat("a", 32)); err == nil {
			t.Fatal("accepted unbounded job")
		}
	}
	if _, err := LatencyJob("unknown", 0); err == nil {
		t.Fatal("reading missing job created work")
	}
}
func TestLatencyJobSurvivesRequestCancellationIsIdempotentAndRedacted(t *testing.T) {
	ids := jobFixture(t)
	parseLatencyTarget = func(ctx context.Context, _ []string, method, network string) (*dialer.TcpCheckOption, error) {
		return dialer.ParseTcpCheckOption(ctx, []string{"http://127.0.0.1", "127.0.0.1"}, method, network)
	}
	ctx, cancel := context.WithCancel(context.Background())
	raw, err := StartLatencyJob(ctx, ids, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	var first testJobStatus
	_ = json.Unmarshal([]byte(raw), &first)
	s := waitJob(t, first.ID)
	if s.Phase != "completed" || s.Completed != 4 || len(s.Results) != 4 {
		t.Fatal("request cancellation lost job", s.Phase, s.Completed)
	}
	retry, err := StartLatencyJob(context.Background(), ids, strings.Repeat("b", 32))
	if err != nil || !strings.Contains(retry, first.ID) {
		t.Fatal("lost idempotent operation")
	}
	raw, err = LatencyJob(first.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var partial testJobStatus
	_ = json.Unmarshal([]byte(raw), &partial)
	if len(partial.Results) != 2 || partial.Cursor != 4 {
		t.Fatal("incremental cursor incorrect")
	}
	if strings.Contains(raw, "private-fixture") || strings.Contains(raw, "Fingerprint") || strings.Contains(raw, "Link") {
		t.Fatal("job leaked private model fields")
	}
}
func TestLatencyJobCancelDrainsAndLeavesUnfinishedNodesForResume(t *testing.T) {
	ids := jobFixture(t)
	started := make(chan struct{})
	parseLatencyTarget = func(ctx context.Context, _ []string, _, _ string) (*dialer.TcpCheckOption, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	raw, err := StartLatencyJob(context.Background(), ids, strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	var s testJobStatus
	_ = json.Unmarshal([]byte(raw), &s)
	<-started
	if _, err = StartLatencyJob(context.Background(), ids, strings.Repeat("d", 32)); err == nil || err.Error() != "latency_job_busy" {
		t.Fatal("allowed overlapping job", err)
	}
	if _, err = CancelLatencyJob(s.ID); err != nil {
		t.Fatal(err)
	}
	s = waitJob(t, s.ID)
	if s.Phase != "cancelled" || s.Completed != 0 || len(s.Results) != 0 {
		t.Fatal("cancelled pending nodes were marked dead")
	}
}
