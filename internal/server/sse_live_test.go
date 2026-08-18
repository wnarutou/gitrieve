package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/config"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/executor"
	"github.com/wnarutou/gitrieve/internal/logger"
	server "github.com/wnarutou/gitrieve/internal/server"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

// TestSSELogStreamingDeliversIncrementally reproduces the reported symptom
// ("only the start log shows, the rest appear only when the job completes")
// through the complete pipeline: logger INSERT → SSE poll → EventSource body.
//
// Logs are inserted across several 1s SSE poll cycles; the job is then marked
// completed so the stream ends with a "done" event and every surviving log row
// is flushed. Arrival timestamps tell us whether rows stream in as they are
// written (healthy), are batched until the stream ends (SSE/DB buffering bug),
// or never arrive at all (silently dropped on write failure).
func TestSSELogStreamingDeliversIncrementally(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gitrieve.db")

	testDB, err := db.Initialize(dbPath)
	require.NoError(t, err)
	defer testDB.Close()

	cfg := &config.Config{
		Repository: []typedef.Repository{{Name: "test-repo", URL: "github.com/test/repo"}},
	}
	log := logger.NewLogger(testDB)
	exec := executor.NewExecutor(log, testDB, cfg)
	s := server.NewTestServerWithExecutor(testDB, exec)
	httpSrv := httptest.NewServer(s)
	defer httpSrv.Close()

	const jobID = "job-live"
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		jobID, "test-repo", time.Now(), "running")
	require.NoError(t, err)

	// First log is already present before the stream opens.
	require.NoError(t, log.Log(jobID, "test-repo", "info", "log-1"))

	// Producer inserts logs spread across several SSE poll cycles, then marks
	// the job completed so the stream ends with "done".
	type step struct {
		at time.Duration
		fn func()
	}
	steps := []step{
		{at: 300 * time.Millisecond, fn: func() { _ = log.Log(jobID, "test-repo", "info", "log-2") }},
		{at: 900 * time.Millisecond, fn: func() { _ = log.Log(jobID, "test-repo", "info", "log-3") }},
		{at: 1500 * time.Millisecond, fn: func() { _ = log.Log(jobID, "test-repo", "info", "log-4") }},
		{at: 2300 * time.Millisecond, fn: func() {
			_, _ = testDB.Exec(`UPDATE executions SET status='completed', end_time=? WHERE id=?`, time.Now(), jobID)
		}},
	}

	// Reader: connect and record arrival time of every data line / done event.
	type rec struct {
		at  time.Duration
		raw string
	}
	var mu sync.Mutex
	var lines []rec
	gotDone := false
	var gotDoneAt time.Duration
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		resp, err := http.Get(httpSrv.URL + "/api/jobs/" + jobID + "/logs")
		if err != nil {
			t.Errorf("SSE connect: %v", err)
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		start := time.Now()
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "data:"):
				mu.Lock()
				lines = append(lines, rec{at: time.Since(start), raw: strings.TrimSpace(line[len("data:"):])})
				mu.Unlock()
			case line == "event: done":
				mu.Lock()
				gotDone = true
				gotDoneAt = time.Since(start)
				mu.Unlock()
			}
		}
	}()

	// Run the producer steps on a schedule.
	go func() {
		for _, st := range steps {
			time.Sleep(st.at)
			st.fn()
		}
	}()

	// The stream should end when the job is completed + done is flushed. Guard
	// against a silent deadlock so this fails fast instead of hanging.
	select {
	case <-readerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("SSE stream did not end after 10s — possible deadlock")
	}

	// Ground truth: which rows actually reached the logs table?
	ground := map[string]bool{}
	if rows, err := testDB.Query(`SELECT message FROM logs WHERE execution_id = ?`, jobID); err == nil {
		for rows.Next() {
			var m string
			if rows.Scan(&m) == nil {
				ground[m] = true
			}
		}
		rows.Close()
	}
	t.Logf("rows in logs table: %v", ground)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, gotDone, "expected a done event to end the stream")

	// Map message -> earliest arrival, extracting the message from each data line.
	arrival := map[string]time.Duration{}
	for _, l := range lines {
		var entry server.LogEntry
		if err := json.Unmarshal([]byte(l.raw), &entry); err != nil {
			continue
		}
		if _, ok := arrival[entry.Message]; !ok {
			arrival[entry.Message] = l.at
		}
	}

	t.Logf("done event at %v", gotDoneAt)
	for _, name := range []string{"log-1", "log-2", "log-3", "log-4"} {
		if arr, ok := arrival[name]; ok {
			t.Logf("%s arrived at %v", name, arr)
		} else {
			t.Errorf("log %q never arrived (dropped on write or never flushed)", name)
		}
	}

	// The bug: every log after the first arrives only at the end, i.e. at (or
	// near) the done event. In a healthy stream log-2 (inserted at 300ms) must
	// be delivered on an earlier poll than the terminal flush.
	if arr2, ok := arrival["log-2"]; ok && gotDone {
		if arr2 >= gotDoneAt-time.Millisecond {
			t.Errorf("log-2 only arrived at the end (%v, done at %v) — logs are buffered until job completion", arr2, gotDoneAt)
		}
	}
}
