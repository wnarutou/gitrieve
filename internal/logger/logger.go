package logger

import (
	_ "github.com/mattn/go-sqlite3"
	"github.com/wnarutou/gitrieve/internal/db"
)

type Logger struct {
	db *db.DB
}

func NewLogger(db *db.DB) *Logger {
	return &Logger{db: db}
}

func (l *Logger) Log(executionID, jobName, level, message string) error {
	if l.db == nil {
		return nil
	}

	_, err := l.db.Exec(`
		INSERT INTO logs (execution_id, timestamp, level, message)
		VALUES (?, datetime('now'), ?, ?)
	`, executionID, level, message)

	return err
}