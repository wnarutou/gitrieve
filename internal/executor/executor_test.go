package executor

import (
	"testing"
	"time"
	"github.com/stretchr/testify/assert"
	"github.com/wnarutou/gitrieve/internal/typedef"
)

func TestExecuteJob(t *testing.T) {
	executor := NewExecutor(nil)

	job := typedef.Repository{
		Name: "test-repo",
		URL:  "https://github.com/test/repo.git",
	}

	execution, err := executor.ExecuteJob(job)
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.Equal(t, "test-repo", execution.JobName)
	assert.NotEmpty(t, execution.ID)
	assert.True(t, execution.StartTime.Before(time.Now()) || execution.StartTime.Equal(time.Now()))
	assert.Equal(t, "success", execution.Status)
}