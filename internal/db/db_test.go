package db

import (
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