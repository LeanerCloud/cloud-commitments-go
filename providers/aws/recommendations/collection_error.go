package recommendations

import (
	"context"
	"errors"
	"fmt"
)

// IncompleteRecommendationsError accompanies survivors of an incomplete collection.
// Each cause represents one rejected RI detail or one failed collection scope.
type IncompleteRecommendationsError struct {
	FailedDetails int
	FailedScopes  int
	Causes        []error
}

func (e *IncompleteRecommendationsError) Error() string {
	return fmt.Sprintf("incomplete AWS recommendations: %d failed details, %d failed scopes: %v",
		e.FailedDetails, e.FailedScopes, errors.Join(e.Causes...))
}

func (e *IncompleteRecommendationsError) Unwrap() []error { return e.Causes }

// addFailure returns whether the failed scope received a usable response.
func (e *IncompleteRecommendationsError) addFailure(err error) bool {
	var partial *IncompleteRecommendationsError
	if errors.As(err, &partial) {
		e.FailedDetails += partial.FailedDetails
		e.FailedScopes += partial.FailedScopes
		e.Causes = append(e.Causes, partial.Causes...)
		return true
	}
	e.FailedScopes++
	e.Causes = append(e.Causes, err)
	return false
}

func collectionCancellation(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
