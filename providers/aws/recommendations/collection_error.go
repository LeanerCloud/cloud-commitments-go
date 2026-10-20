package recommendations

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotUSD marks a detail skipped because Cost Explorer priced it in a
// non-USD currency; common.Recommendation costs are USD by contract. It
// surfaces through IncompleteRecommendationsError.Causes.
var ErrNotUSD = errors.New("recommendation amounts are not denominated in USD")

// requireUSD rejects an explicit non-USD CurrencyCode. An absent or blank code
// is accepted: CE fixtures omit it and the field is documented as optional.
func requireUSD(code string) error {
	code = strings.TrimSpace(code)
	if code == "" || strings.EqualFold(code, "USD") {
		return nil
	}
	return fmt.Errorf("%w: CurrencyCode %q", ErrNotUSD, code)
}

// IncompleteRecommendationsError accompanies survivors of an incomplete collection.
// Each cause represents one rejected detail or one failed collection scope.
type IncompleteRecommendationsError struct {
	Causes        []error
	FailedDetails int
	FailedScopes  int
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
