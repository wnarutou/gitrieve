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

func TestSkippedReasonRecognizesPointer(t *testing.T) {
	reason, ok := SkippedReason(&SkippedError{Reason: "repository has no wiki"})

	require.True(t, ok)
	require.Equal(t, "repository has no wiki", reason)
}

func TestSkippedReasonRecognizesWrappedPointer(t *testing.T) {
	err := fmt.Errorf("wiki preflight: %w", &SkippedError{Reason: "repository has no wiki"})
	reason, ok := SkippedReason(err)

	require.True(t, ok)
	require.Equal(t, "repository has no wiki", reason)
}

func TestSkippedReasonRejectsNilPointer(t *testing.T) {
	var skipped *SkippedError

	_, ok := SkippedReason(skipped)
	require.False(t, ok)
}
