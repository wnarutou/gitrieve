package syncresult

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkippedReasonSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("wiki preflight: %w", Skip("repository has no wiki"))
	reason, ok := SkippedReason(err)
	require.True(t, ok)
	require.Equal(t, "repository has no wiki", reason)

	_, ok = SkippedReason(errors.New("network down"))
	require.False(t, ok)
}
