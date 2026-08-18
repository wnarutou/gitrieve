package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatabaseInitialization(t *testing.T) {
	db, err := Initialize(":memory:")
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// Test tables exist
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count)
	assert.NoError(t, err)
	assert.Greater(t, count, 0)
}

// TestFileDBEnablesWALAndBusyTimeout guards the fix for silently dropped log
// lines: a file-backed DB must run in WAL mode with a busy timeout so the SSE
// log-stream reader and the job-goroutine log writers can operate concurrently
// without "database is locked (SQLITE_BUSY)" write failures.
func TestFileDBEnablesWALAndBusyTimeout(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gitrieve.db")
	testDB, err := Initialize(dbPath)
	assert.NoError(t, err)
	defer testDB.Close()

	var mode string
	err = testDB.QueryRow("PRAGMA journal_mode").Scan(&mode)
	assert.NoError(t, err)
	assert.Equal(t, "wal", strings.ToLower(mode), "file-backed DB must run in WAL journal mode")

	var timeout int
	err = testDB.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	assert.NoError(t, err)
	assert.Equal(t, 5000, timeout, "busy_timeout must be 5000ms")
}