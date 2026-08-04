package logger

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/wnarutou/gitrieve/internal/db"
)

func TestLoggerLog(t *testing.T) {
	// Create in-memory database
	db, err := db.Initialize(":memory:")
	assert.NoError(t, err)

	logger := NewLogger(db)

	// Test logging
	err = logger.Log("test-execution-id", "test-job", "info", "Test message")
	assert.NoError(t, err)

	// Verify log was stored
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count)
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}