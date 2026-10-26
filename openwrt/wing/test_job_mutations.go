// SPDX-License-Identifier: AGPL-3.0-only
package graphql

import (
	"context"
	"github.com/daeuniverse/dae-wing/graphql/service/node"
	"github.com/graph-gophers/graphql-go"
)

func (r *MutationResolver) StartLatencyTest(ctx context.Context, args struct {
	IDs       []graphql.ID
	Operation string
}) (string, error) {
	return node.StartLatencyJob(ctx, args.IDs, args.Operation)
}
func (r *MutationResolver) StopLatencyTest(args struct{ ID string }) (int32, error) {
	return node.CancelLatencyJob(args.ID)
}
func (r *queryResolver) LatencyTest(args struct {
	ID    string
	After *int32
}) (string, error) {
	after := 0
	if args.After != nil {
		after = int(*args.After)
	}
	return node.LatencyJob(args.ID, after)
}
