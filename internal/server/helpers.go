package server

import "strings"

// escapeLike escapes LIKE metacharacters so user input is matched literally
// when used with ESCAPE '\'.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
