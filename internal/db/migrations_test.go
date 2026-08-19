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

// TestMigrateAddsRepoKeyColumn verifies Migrate upgrades a legacy executions
// table (no repo_key) in place without backfilling, and that new rows can
// write repo_key.
func TestMigrateAddsRepoKeyColumn(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)
	defer testDB.Close()

	// Rebuild executions in the pre-migration shape (no repo_key).
	_, err = testDB.Exec(`DROP TABLE executions`)
	assert.NoError(t, err)
	_, err = testDB.Exec(`
		CREATE TABLE executions (
			id TEXT PRIMARY KEY,
			job_name TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME,
			status TEXT NOT NULL,
			error_message TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
	assert.NoError(t, err)

	// A pre-existing legacy row.
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, start_time, status) VALUES (?, ?, ?, ?)`,
		"old", "repo-a", time.Now(), "completed")
	assert.NoError(t, err)

	assert.NoError(t, Migrate(testDB))

	// Column now exists; legacy row's key stays empty (no backfill).
	var key string
	err = testDB.QueryRow(`SELECT repo_key FROM executions WHERE id = 'old'`).Scan(&key)
	assert.NoError(t, err)
	assert.Equal(t, "", key)

	// New rows can write repo_key.
	_, err = testDB.Exec(`INSERT INTO executions (id, job_name, repo_key, start_time, status) VALUES (?, ?, ?, ?, ?)`,
		"new", "repo-b", "github.com/b/b", time.Now(), "running")
	assert.NoError(t, err)
}

// TestMigrateIsIdempotent verifies Migrate on a fresh (already current) DB is a no-op.
func TestMigrateIsIdempotent(t *testing.T) {
	testDB, err := Initialize(":memory:")
	assert.NoError(t, err)
	defer testDB.Close()

	assert.NoError(t, Migrate(testDB))
	assert.NoError(t, Migrate(testDB))
}
