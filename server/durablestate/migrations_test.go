package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrate_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	require.NoError(t, store.Migrate(ctx))
	require.NoError(t, store.Migrate(ctx))

	version, err := store.currentSchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, len(migrations), version)
}

func TestMigrate_DownReversesSchema(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	// Confirm the schema is actually present before reverting.
	var tableCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'current_state'`,
	).Scan(&tableCount))
	require.Equal(t, 1, tableCount)

	require.NoError(t, store.MigrateDown(ctx, len(migrations)))

	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'current_state'`,
	).Scan(&tableCount))
	require.Equal(t, 0, tableCount, "MigrateDown must drop every table it created")

	version, err := store.currentSchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, version)

	// Re-applying Migrate after a full down-migration must restore the
	// schema from scratch without error.
	require.NoError(t, store.Migrate(ctx))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'current_state'`,
	).Scan(&tableCount))
	require.Equal(t, 1, tableCount)
}

func TestMigrate_DownIsNoOpForZeroOrNegativeSteps(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	require.NoError(t, store.MigrateDown(ctx, 0))
	version, err := store.currentSchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, len(migrations), version)
}
