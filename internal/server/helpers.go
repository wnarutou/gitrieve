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
	if cronExpr == "" {
		return nil
	}
	sched, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return nil
	}
	t := sched.Next(now)
	return &t
}
