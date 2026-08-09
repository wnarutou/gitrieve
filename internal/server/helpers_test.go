package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, "repo", escapeLike("repo"))
	assert.Equal(t, "100\\%", escapeLike("100%"))
	assert.Equal(t, "a\\_b", escapeLike("a_b"))
	assert.Equal(t, "a\\\\b", escapeLike(`a\b`))
}
