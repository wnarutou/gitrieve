package syncresult

import "errors"

type SkippedError struct {
	Reason string
}

func (e SkippedError) Error() string {
	return e.Reason
}

func Skip(reason string) error {
	return SkippedError{Reason: reason}
}

func SkippedReason(err error) (string, bool) {
	var skipped SkippedError
	if errors.As(err, &skipped) {
		return skipped.Reason, true
	}

	var skippedPointer *SkippedError
	if errors.As(err, &skippedPointer) && skippedPointer != nil {
		return skippedPointer.Reason, true
	}

	return "", false
}
