package server_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wnarutou/gitrieve/internal/db"
	"github.com/wnarutou/gitrieve/internal/logger"
	server "github.com/wnarutou/gitrieve/internal/server"
)

// TestLogScanAfterInsert verifies that a log row committed via the logger is
// readable and scannable by the exact query the SSE handler runs. If the
// modernc driver cannot scan the stored DATETIME into time.Time, every SSE
// poll silently aborts (rows.Scan error → return true) and no logs are ever
// streamed.
func TestLogScanAfterInsert(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gitrieve.db")
	testDB, err := db.Initialize(dbPath)
	require.NoError(t, err)
	defer testDB.Close()

	log := logger.NewLogger(testDB)
	require.NoError(t, log.Log("job-scan", "test-repo", "info", "hello-world"))

	rows, err := testDB.Query(
		"SELECT id, execution_id, timestamp, level, message FROM logs WHERE execution_id = ? AND id > 0 ORDER BY id ASC",
		"job-scan")
	require.NoError(t, err)
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
		var entry server.LogEntry
		err := rows.Scan(&entry.ID, &entry.ExecutionID, &entry.Timestamp, &entry.Level, &entry.Message)
		t.Logf("scan err=%v entry=%+v", err, entry)
		require.NoError(t, err)
		require.Equal(t, "hello-world", entry.Message)
		require.False(t, entry.Timestamp.IsZero())
	}
	require.Equal(t, 1, count)
	_ = time.Now()
}
