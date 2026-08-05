package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestInitializeCreatesTables verifies that Initialize creates the executions
// and logs tables with the expected columns.
func TestInitializeCreatesTables(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)
	defer testDB.Close()

	// executions table should exist and accept inserts
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"exec-1", "test-job", time.Now(), "running")
	assert.NoError(t, err)

	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM executions").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)

	// logs table should exist and accept inserts with a foreign key to executions
	_, err = testDB.Exec(`INSERT INTO logs (execution_id, timestamp, level, message) VALUES (?, ?, ?, ?)`,
		"exec-1", time.Now(), "info", "Test log message")
	assert.NoError(t, err)

	err = testDB.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestInitializeIsIdempotent verifies that calling Initialize on an existing
// database does not error (CREATE TABLE IF NOT EXISTS).
func TestInitializeIsIdempotent(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)

	// Re-running the same schema statement should not fail
	_, err = testDB.Exec(`
		CREATE TABLE IF NOT EXISTS executions (
			id TEXT PRIMARY KEY,
			job_name TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME,
			status TEXT NOT NULL,
			error_message TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
	assert.NoError(t, err)
}
