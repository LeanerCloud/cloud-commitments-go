package insurance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"time"
)

// RecommendationQuoteClient is the read-only vendor surface QuoteRecommendation needs: the published
// QuoteClient plus the line-item read it lacks. Client implements it.
type RecommendationQuoteClient interface {
	QuoteClient
	LineItems(ctx context.Context, planID string) ([]LineItem, error)
}

// RecommendationQuoteStatus is the outcome of quoting one stored recommendation.
type RecommendationQuoteStatus string

const (
	// QuoteStatusQuote means exactly one line item matched exactly and its figures are present.
	QuoteStatusQuote RecommendationQuoteStatus = "quote"
	// QuoteStatusUnavailable means no exact match could be proven; no figure is returned.
	QuoteStatusUnavailable RecommendationQuoteStatus = "unavailable"
	// QuoteStatusAmbiguous means more than one line item matches the recommendation.
	QuoteStatusAmbiguous RecommendationQuoteStatus = "ambiguous"
	// QuoteStatusIneligible means the recommendation is not a product this matcher supports; the vendor
	// is never called for it.
	QuoteStatusIneligible RecommendationQuoteStatus = "ineligible"
)

// RecommendationQuote is the typed result. Comparison is set only for QuoteStatusQuote.
type RecommendationQuote struct {
	Comparison *RecommendationComparisonDTO
	Status     RecommendationQuoteStatus
	Reason     Reason
}

// QuoteErrorKind classifies a failed quote so callers can map each kind to fixed user text.
type QuoteErrorKind string

const (
	QuoteErrInvalidRequest    QuoteErrorKind = "invalid_request"
	QuoteErrCanceled          QuoteErrorKind = "canceled"
	QuoteErrTimeout           QuoteErrorKind = "timeout"
	QuoteErrRejected          QuoteErrorKind = "vendor_rejected"
	QuoteErrVendorUnavailable QuoteErrorKind = "vendor_unavailable"
	QuoteErrInvalidResponse   QuoteErrorKind = "invalid_response"
	// QuoteErrUnrepresentable means a vendor number has no exact decimal form.
	QuoteErrUnrepresentable QuoteErrorKind = "unrepresentable_value"
)

// QuoteError is the only error QuoteRecommendation returns. Its text is fixed and never contains vendor,
// plan or credential text; the vendor cause is dropped, not wrapped. Status and RetryAfter are set for
// vendor HTTP failures.
type QuoteError struct {
	Kind       QuoteErrorKind
	Stage      string
	Status     int
	RetryAfter time.Duration
}

func (e *QuoteError) Error() string {
	return fmt.Sprintf("archera recommendation quote failed: %s at %s", e.Kind, e.Stage)
}

// Unwrap exposes only the context sentinel, so errors.Is(err, context.Canceled) keeps working.
func (e *QuoteError) Unwrap() error {
	switch e.Kind {
	case QuoteErrCanceled:
		return context.Canceled
	case QuoteErrTimeout:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

const (
	stageRequest    = "request"
	stageLineItems  = "line_items"
	stageComparison = "comparison"
	stageBuild      = "build"
)

// QuoteRecommendation reads every line item, selects the recommendation's own one and reads a comparison
// scoped to it for every payment option. Only an exact, unique match with a proven comparison basis yields
// figures; anything else is unavailable, ambiguous or ineligible. Each vendor read is a single call with no
// retry, and a failure returns no partial result.
//
// Callers map these knowingly: an ineligible recommendation returns a result with no vendor call (the
// platform route returned an empty comparison); only the quote status carries a DTO, so unavailable and
// ambiguous results have none; a context deadline or client timeout is QuoteErrTimeout while a cancellation
// is QuoteErrCanceled (the platform route reported both as a timeout). recommendationID and planID must be UUIDs.
func QuoteRecommendation(ctx context.Context, client RecommendationQuoteClient, planID, recommendationID string, rec *RecommendationIdentity) (*RecommendationQuote, error) {
	if isNilClient(client) || !validMatchUUID(planID) || !validMatchUUID(recommendationID) {
		return nil, &QuoteError{Kind: QuoteErrInvalidRequest, Stage: stageRequest}
	}
	if !validRecommendation(rec) {
		return &RecommendationQuote{Status: QuoteStatusIneligible, Reason: ReasonIdentityUnproven}, nil
	}
	items, selected, early, err := readSelectedLineItem(ctx, client, planID, rec)
	if err != nil || early != nil {
		return early, err
	}
	if err = ctx.Err(); err != nil {
		return nil, quoteFailure(ctx, err, stageComparison)
	}
	cmp, err := client.Comparison(ctx, ComparisonRequest{PlanID: planID, LineItemIDs: []string{selected},
		PaymentOptions: []PaymentOption{PaymentNoUpfront, PaymentPartialUpfront, PaymentAllUpfront}})
	if err != nil {
		return nil, quoteFailure(ctx, err, stageComparison)
	}
	return quoteFromComparison(recommendationID, planID, rec, cmp, items)
}

// quoteFromComparison renders figures only when the comparison proves the exact match.
func quoteFromComparison(recommendationID, planID string, rec *RecommendationIdentity, cmp *Comparison, items []LineItem) (*RecommendationQuote, error) {
	if cmp == nil {
		return nil, &QuoteError{Kind: QuoteErrInvalidResponse, Stage: stageComparison}
	}
	dto, err := BuildRecommendationComparison(recommendationID, planID, rec, cmp, items)
	if errors.Is(err, ErrUnrepresentable) {
		return nil, &QuoteError{Kind: QuoteErrUnrepresentable, Stage: stageBuild}
	}
	if err != nil {
		return nil, &QuoteError{Kind: QuoteErrInvalidResponse, Stage: stageBuild}
	}
	if dto.Status != ResourceAssociated {
		return &RecommendationQuote{Status: QuoteStatusUnavailable, Reason: dto.Reason}, nil
	}
	return &RecommendationQuote{Status: QuoteStatusQuote, Comparison: dto}, nil
}

// readSelectedLineItem reads the line items and selects the recommendation's own one. A non-nil early result
// means no unique line item exists, so no comparison is read.
func readSelectedLineItem(ctx context.Context, client RecommendationQuoteClient, planID string, rec *RecommendationIdentity) (items []LineItem, selected string, early *RecommendationQuote, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nil, quoteFailure(ctx, err, stageLineItems)
	}
	items, err = client.LineItems(ctx, planID)
	if err != nil {
		return nil, "", nil, quoteFailure(ctx, err, stageLineItems)
	}
	identities := make([]LineItemIdentity, 0, len(items))
	for i := range items {
		identities = append(identities, contractLineItem(&items[i]))
	}
	selected, status := SelectLineItem(rec, identities)
	switch status {
	case SelectNone:
		return items, "", &RecommendationQuote{Status: QuoteStatusUnavailable, Reason: ReasonIdentityUnproven}, nil
	case SelectAmbiguous:
		return items, "", &RecommendationQuote{Status: QuoteStatusAmbiguous, Reason: ReasonAmbiguousLineItems}, nil
	}
	return items, selected, nil, nil
}

func isNilClient(c RecommendationQuoteClient) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// quoteFailure maps a vendor or transport error to a QuoteError, dropping its text.
func quoteFailure(ctx context.Context, err error, stage string) *QuoteError {
	out := &QuoteError{Stage: stage}
	var he *HTTPError
	// context.DeadlineExceeded is itself a timeout net.Error.
	var ne net.Error
	switch {
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		out.Kind = QuoteErrCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		out.Kind = QuoteErrTimeout
	case errors.As(err, &he):
		out.Status, out.RetryAfter = he.StatusCode, he.RetryAfter
		out.Kind = QuoteErrRejected
		if he.StatusCode >= 500 {
			out.Kind = QuoteErrVendorUnavailable
		}
	case errors.As(err, &ne):
		out.Kind = QuoteErrVendorUnavailable
	default:
		out.Kind = QuoteErrInvalidResponse
	}
	return out
}
