// SPDX-License-Identifier: AGPL-3.0-only
package control

import "context"

// Each serving generation owns duplicated descriptors. Cancelling its context
// wakes blocked Accept/UDP reads without closing the shared handoff listeners
// or consuming a new generation's first connection with a cancelled context.
func routerOwnServingListener(ctx context.Context, base *Listener) (*Listener, func(), error) {
	owned, err := base.Clone()
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = owned.Close() })
	return owned, func() { stop(); _ = owned.Close() }, nil
}
