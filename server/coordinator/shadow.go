package coordinator

import (
	"context"

	"kandev-plugin-coordinator/server/governor"
)

// ShadowStoreObserver is the narrow adapter used by an embedding runtime. It
// owns no scheduler or Host transport: observations remain supplied snapshots.
type ShadowStoreObserver struct {
	Store *governor.Store
	Fence int64
}

func (o ShadowStoreObserver) Observe(ctx context.Context, input governor.Observation) (governor.Result, error) {
	return o.Store.Observe(ctx, o.Fence, input)
}
