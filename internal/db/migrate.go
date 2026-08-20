package db

import "fmt"

// Migrate upgrades an existing database to the current schema. Additive-only:
// it adds the repo_key column to executions when missing and never backfills or
// drops data. Call once at server startup (after Initialize).
func Migrate(d *DB) error {
	has, err := columnExists(d, "executions", "repo_key")
	if err != nil {
		return fmt.Errorf("check executions.repo_key: %w", err)
	}
	if !has {
		if _, err := d.Exec(`ALTER TABLE executions ADD COLUMN repo_key TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add executions.repo_key: %w", err)
		}
	}
	return nil
}

// columnExists reports whether table has a column named column.
func columnExists(d *DB, table, column string) (bool, error) {
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             *string
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
