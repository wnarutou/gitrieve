package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/syncresult"
	"github.com/wnarutou/gitrieve/internal/ui"
)

type logRecord struct {
	level   string
	message string
}

type logSink struct {
	logs []logRecord
}

func (s *logSink) Log(_, _, level, message string) error {
	s.logs = append(s.logs, logRecord{level: level, message: message})
	return nil
}

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

func TestRunStaggeredSkipCompletesWithoutFailureOrRetry(t *testing.T) {
	previousConfig := config.GetIns()
	config.SetIns(&config.Config{GitHubScheduleJitter: 0})
	if previousConfig != nil {
		t.Cleanup(func() { config.SetIns(previousConfig) })
	}

	sink := &logSink{}
	ui.SetSink(sink)
	t.Cleanup(func() { ui.SetSink(nil) })
	unbind := ui.Bind("execution-1", "wiki")
	defer unbind()

	calls := 0
	runStaggered(context.Background(), func() error {
		calls++
		return syncresult.Skip("not available")
	})

	require.Equal(t, 1, calls)
	require.Equal(t, []logRecord{{level: "info", message: "Skipped: not available"}}, sink.logs)
}
