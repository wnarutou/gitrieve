package server

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// escapeLike escapes LIKE metacharacters so user input is matched literally
// when used with ESCAPE '\'.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// nextRunTime returns the next time a repository's cron expression will fire,
// or nil when the expression is empty or invalid.
func nextRunTime(cronExpr string, now time.Time) *time.Time {
	sched, err := parseRepositorySchedule(cronExpr)
	if err != nil {
		return nil
	}
	if sched == nil {
		return nil
	}
	t := sched.Next(now)
	return &t
}

func parseRepositorySchedule(cronExpr string) (cron.Schedule, error) {
	if cronExpr == "" {
		return nil, nil
	}
	return cron.ParseStandard(cronExpr)
}
