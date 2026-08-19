package repository

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/wnarutou/gitrieve/internal/ui"
)

// progressWriter routes go-git clone/fetch/pull progress into the log sink so
// long-running network operations stream live status to the UI. It replaces
// the previous Progress: os.Stdout, which wrote to the process stdout —
// visible in a terminal, but invisible to the web UI and the daemon, leaving
// the log stream silent for the entire clone/fetch.
//
// go-git emits a progress update per object, frequently as \r-separated
// in-place updates (thousands of lines for a large clone). To keep the logs
// table bounded, at most one line per second is emitted; the terminal "done"
// line of each phase always passes so the stream ends on a meaningful state.
//
// Progress writes happen on the goroutine that runs the clone/fetch/pull
// (the job goroutine, which ui.Bind has associated with the execution), so
// ui.Printf forwards them through the sink into the DB.
type progressWriter struct {
	mu       sync.Mutex
	buf      []byte
	last     time.Time
	interval time.Duration // emit throttle; zero means the 1s default
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		j := bytes.IndexByte(w.buf, '\r')
		var idx int
		switch {
		case i >= 0 && (j < 0 || i < j):
			idx = i
		case j >= 0:
			idx = j
		default:
			// No complete segment yet — buffer the partial line and wait.
			return len(p), nil
		}
		seg := strings.TrimSpace(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
		if seg != "" {
			w.emit(seg)
		}
	}
}

// emit throttles progress lines to one per interval (1s by default); a "done"
// line always passes so the stream ends on a meaningful state.
func (w *progressWriter) emit(line string) {
	iv := w.interval
	if iv == 0 {
		iv = time.Second
	}
	now := time.Now()
	if strings.Contains(line, "done") || now.Sub(w.last) >= iv {
		ui.Printf("%s\n", line)
		w.last = now
	}
}
