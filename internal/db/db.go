package db

import (
	"database/sql"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGo required
)

type DB struct {
	*sql.DB
}

func Initialize(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// An in-memory SQLite database lives per-connection, so a connection pool
	// would hand out multiple unrelated databases (schema created on one, empty
	// on the rest). Pin the pool to a single connection for ":memory:" so tests
	// that concurrently read and write (job goroutines + assertions) see a
	// coherent database. File-backed databases are shared across connections
	// and need no such pinning.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS executions (
			id TEXT PRIMARY KEY,
			job_name TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME,
			status TEXT NOT NULL,
			error_message TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			execution_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			level TEXT NOT NULL,
			message TEXT NOT NULL,
			FOREIGN KEY (execution_id) REFERENCES executions(id)
		);
	`)

	return &DB{db}, err
}