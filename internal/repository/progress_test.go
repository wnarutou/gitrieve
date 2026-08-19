package repository

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/ui"
)

// recSink captures messages forwarded through the ui sink, like the SSE log
// stream does in production.
type recSink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *recSink) Log(executionID, jobName, level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, message)
	return nil
}

func (s *recSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.msgs))
	copy(out, s.msgs)
	return out
}

// TestProgressWriterForwardsToSink verifies the whole chain Fix 2 relies on:
// progress bytes written through progressWriter reach the log sink (via
// ui.Printf on the bound goroutine) as complete, trimmed lines, instead of
// going to os.Stdout where the web UI never sees them.
func TestProgressWriterForwardsToSink(t *testing.T) {
	sink := &recSink{}
	ui.SetSink(sink)
	unbind := ui.Bind("exec-progress", "test-repo")
	defer func() {
		unbind()
		ui.SetSink(nil)
	}()

	w := &progressWriter{}
	// Simulated server progress: a \r-separated in-place update followed by
	// \n-terminated lines, the last one being the "done" marker.
	data := []byte("remote: Enumerating objects: 50%\rremote: Counting objects: 100% (287/287)\nremote: Counting objects: 100% (287/287), done.\n")
	n, err := w.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	got := sink.snapshot()
	require.NotEmpty(t, got, "bound-goroutine progress must reach the sink")
	for _, m := range got {
		require.Contains(t, m, "remote:", "each emitted line is a complete, trimmed progress line")
	}
}

// TestProgressWriterThrottlesAndPassesDone verifies the throttle: at most one
// line per interval is emitted, except "done" lines which always pass — keeping
// the logs table bounded during long clones.
func TestProgressWriterThrottlesAndPassesDone(t *testing.T) {
	sink := &recSink{}
	ui.SetSink(sink)
	unbind := ui.Bind("exec-progress", "test-repo")
	defer func() {
		unbind()
		ui.SetSink(nil)
	}()

	// A huge throttle window so no real waiting is needed in the test.
	w := &progressWriter{interval: time.Hour}

	// A burst of \n-terminated lines within one Write call.
	burst := []byte("remote: a\nremote: b\nremote: c\nremote: done.\n")
	n, err := w.Write(burst)
	require.NoError(t, err)
	require.Equal(t, len(burst), n)
	require.Equal(t, []string{"remote: a", "remote: done."}, sink.snapshot(),
		"only the first burst line and the done line should be emitted")

	// Reset the throttle window: the next line must pass again.
	w.mu.Lock()
	w.last = time.Time{}
	w.mu.Unlock()
	_, err = w.Write([]byte("remote: d\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"remote: a", "remote: done.", "remote: d"}, sink.snapshot())
}
