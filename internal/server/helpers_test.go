package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, "repo", escapeLike("repo"))
	assert.Equal(t, "100\\%", escapeLike("100%"))
	assert.Equal(t, "a\\_b", escapeLike("a_b"))
	assert.Equal(t, "a\\\\b", escapeLike(`a\b`))
}

func TestNextRunTime(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	assert.Nil(t, nextRunTime("", now), "empty cron has no next run")
	assert.Nil(t, nextRunTime("not a cron", now), "invalid cron has no next run")

	nt := nextRunTime("0 2 * * *", now)
	require.NotNil(t, nt)
	assert.Equal(t, time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC), *nt)

	nt2 := nextRunTime("@daily", now)
	require.NotNil(t, nt2)
	assert.Equal(t, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), *nt2)
}

func TestParseRepositoryScheduleReportsInvalidExpression(t *testing.T) {
	schedule, err := parseRepositorySchedule("")
	require.NoError(t, err)
	assert.Nil(t, schedule)

	schedule, err = parseRepositorySchedule("not a cron")
	require.Error(t, err)
	assert.Nil(t, schedule)

	schedule, err = parseRepositorySchedule("30 2 * * *")
	require.NoError(t, err)
	require.NotNil(t, schedule)

	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	afterDSTJump := schedule.Next(time.Date(2026, time.March, 7, 3, 30, 0, 0, newYork))
	assert.Equal(t, time.Date(2026, time.March, 9, 2, 30, 0, 0, newYork), afterDSTJump)
}
