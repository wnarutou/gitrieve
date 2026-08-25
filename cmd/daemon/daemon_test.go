package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStaggerZeroReturnsImmediately(t *testing.T) {
	require.NoError(t, stagger(context.Background(), 0, func(int64) int64 { t.Fatal("random source called"); return 0 }))
}

func TestStaggerUsesBoundedRandomDelay(t *testing.T) {
	start := time.Now()
	require.NoError(t, stagger(context.Background(), 20*time.Millisecond, func(n int64) int64 {
		require.Equal(t, int64(20*time.Millisecond)+1, n)
		return int64(10 * time.Millisecond)
	}))
	require.GreaterOrEqual(t, time.Since(start), 8*time.Millisecond)
}

func TestStaggerCancellationStopsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, stagger(ctx, time.Hour, func(int64) int64 { return int64(time.Hour) }), context.Canceled)
}
