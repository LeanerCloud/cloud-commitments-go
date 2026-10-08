// Package insurance is the shared bounded context for Archera insured-commitment
// quote previews across the CLI, MCP server, and platform consumers.
//
// Contract basis (public documentation only, no live calls in this package's
// tests):
//   - Operation: GET /v1/org/{org_id}/commitment-plans/{plan_id}/comparison,
//     response schema LineItemOfferComparisonResponse
//     (docs.archera.ai/api-reference/public-api/commitment-plans.md)
//   - Auth: x-api-key header on https://api.archera.ai
//     (docs.archera.ai/api-reference/public-api/api-key-access.md)
//
// Invariants every consumer must preserve:
//   - archera_premium is already included in commitment_cost.total. Money
//     decoded from the API passes through verbatim; this package deliberately
//     has no helper that adds or subtracts a premium from an API total.
//   - All commitment_financials_monthly_rate blocks are 730-hour monthly
//     rates. commitment_upfront_cost is one-time dollars at signing and is
//     never summed with a rate.
//   - The API schemas carry no currency field; Currency stays nil (unknown).
//     Nil money fields are unknown, never silently zero.
//   - No documented quote TTL or expiry exists; FetchedAt records when the
//     data was retrieved and freshness policy belongs to the consumer.
//   - A plan comparison is a hypothetical rollup, never a bindable insurance
//     quote, and never a purchase. An insured target of 100% is a requested
//     target subject to Archera underwriting allowances, not a guarantee. The
//     API exposes no per-line product support or allowance verdict, so
//     consumers render both as unknown.
package insurance

import (
	"context"
	"math/big"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Financials mirrors the documented CommitmentFinancialsNoRebate schema. Every
// field is a 730-hour monthly rate. Any field may be nil: the nested API
// schemas declare no required properties, so absent means unknown, never zero.
type Financials struct {
	// CommitmentCostTotal is the headline monthly cost paid:
	// CloudProviderCost + Premium, with the premium already included.
	CommitmentCostTotal *big.Rat
	// CloudProviderCost is the amortized monthly cost paid to the cloud
	// provider.
	CloudProviderCost *big.Rat
	// Premium is archera_premium. Already included in CommitmentCostTotal;
	// never add it on top or subtract it again.
	Premium *big.Rat
	// GrossSavings is savings before the premium.
	GrossSavings *big.Rat
	// NetSavings is savings after the premium. It can be negative.
	NetSavings *big.Rat
	// CoveredOnDemandCost is the on-demand cost of usage covered by
	// commitments: the savings baseline, not a cost the user pays.
	CoveredOnDemandCost *big.Rat
}

// Totals mirrors LineItemOfferComparisonTotals: plan-wide rollup for a set of
// line items.
type Totals struct {
	// Monthly holds the 730-hour monthly rate financials.
	Monthly Financials
	// UpfrontCost is one-time dollars due at signing (required by the
	// schema). It is NOT a rate and must never be summed with Monthly.
	UpfrontCost *big.Rat
}

// PaymentOption is the comparison schema's payment option enum.
type PaymentOption string

const (
	PaymentNoUpfront      PaymentOption = "no_upfront"
	PaymentPartialUpfront PaymentOption = "partial_upfront"
	PaymentAllUpfront     PaymentOption = "all_upfront"
)

// TermReason explains why a line item landed at its actual term inside a
// hypothetical rollup. Values mirror the documented actual_term_reason enum.
type TermReason string

const (
	// TermReasonExactMatch means the target term was available.
	TermReasonExactMatch TermReason = "exact_match"
	// TermReasonFallbackShorter means the longest available term at or below
	// the target with the same payment option was used.
	TermReasonFallbackShorter TermReason = "fallback_closest_shorter"
	// TermReasonNoAlternative means nothing qualified and the line item kept
	// its current term.
	TermReasonNoAlternative TermReason = "no_alternative"
	// TermReasonBundleMember means the line item belongs to a GPU CUD bundle
	// that cannot be term-swapped per member, so it kept its current term.
	TermReasonBundleMember TermReason = "bundle_member"
)

// HypotheticalLineItem mirrors the documented HypotheticalLineItem schema: how
// one line item actually lands inside a hypothetical rollup. The three
// Actual* identities are nullable in the schema; nil means the vendor sent
// null. It carries no lease, so it never says whether the line is guaranteed.
type HypotheticalLineItem struct {
	LineItemID           string
	ActualTerm           *string
	ActualPaymentOption  *PaymentOption
	ActualCommitmentType *string
	Reason               TermReason
}

// Hypothetical mirrors the documented HypotheticalTotal schema: the plan-wide
// rollup for one (contract term, payment option) combination.
type Hypothetical struct {
	// ContractTerm is nullable in the schema.
	ContractTerm  *string
	PaymentOption PaymentOption
	Totals        Totals
	// Deltas compare this hypothetical against the CURRENT plan, not against
	// on-demand. Positive DeltaMonthlyNetSavings means switching saves more
	// than the plan does today.
	DeltaMonthlyNetSavings     *big.Rat
	DeltaMonthlyCommitmentCost *big.Rat
	// DeltaUpfrontCost is one-time dollars, NOT a rate.
	DeltaUpfrontCost *big.Rat
	LineItems        []HypotheticalLineItem
}

// OfferDelta mirrors OfferComparisonDelta: the vendor's candidate-minus-current
// difference, passed through so no consumer recomputes it.
type OfferDelta struct {
	MonthlyNetSavings *big.Rat
	// UpfrontCost is one-time dollars, NOT a rate.
	UpfrontCost  *big.Rat
	DiscountRate *big.Rat
	// BreakevenDays is nil when either side has no finite breakeven.
	BreakevenDays *big.Rat
}

// OfferEntry mirrors the consumed subset of OfferComparisonEntry: one offer
// (current or candidate) for a line item. Fields that feed the vendor's
// line-item update operation (selected_amount, offer_org_id) are deliberately
// not mapped.
type OfferEntry struct {
	// IsCurrent is true when this entry matches the line item's current
	// offer and lease.
	IsCurrent bool
	OfferID   string
	// CommitmentType is the vendor's offer type string. The docs give both
	// "aws/AmazonEC2"-style and "ri"/"savings_plan"/"cud"-style examples;
	// treat it as opaque display text.
	CommitmentType string
	Provider       common.ProviderType
	// ContractTerm is the effective lock-in term (lease lock-in when a lease
	// is attached). Nullable in the schema.
	ContractTerm *string
	// PaymentOption is nullable in the schema.
	PaymentOption *PaymentOption
	// LeaseMenuItemID is the attached Archera lease, nil for none.
	LeaseMenuItemID *string
	// GuaranteedDisplayName is the offer name when purchased as an Archera
	// Guaranteed Commitment; nil when the vendor sends none.
	GuaranteedDisplayName *string
	// Region is nil when the offer does not report one.
	Region *string
	// DiscountRate is the discount versus on-demand on a 0-1 basis.
	DiscountRate *big.Rat
	// BreakevenDays is nil when breakeven is undefined.
	BreakevenDays *big.Rat
	// Monthly holds the 730-hour monthly rate financials.
	Monthly Financials
	// UpfrontCost is one-time dollars at signing, NOT a rate.
	UpfrontCost *big.Rat
	Delta       OfferDelta
}

// LeaseBacked reports whether an Archera lease is attached to this offer. The
// API does not state that this means guaranteed or insured, and it is not
// evidence of active protection; render it as "lease attached".
func (e OfferEntry) LeaseBacked() bool { return e.LeaseMenuItemID != nil }

// ComparisonRow mirrors LineItemOfferComparisonRow: a line item's current
// offer plus its alternatives, ordered by the vendor (monthly net savings
// descending).
type ComparisonRow struct {
	LineItemID string
	Current    OfferEntry
	Candidates []OfferEntry
}

// Comparison is the decoded result of the documented comparison operation.
// Vendor totals pass through verbatim with the premium already inside them.
// It is a plan hypothetical, not a bindable quote.
type Comparison struct {
	OrgID string
	// PlanID is an Archera plan identifier, not a local recommendation ID.
	PlanID string
	// Current is the plan-wide rollup for the line items currently in scope.
	Current Totals
	// Hypotheticals holds one rollup per requested (or candidate-derived)
	// (contract term, payment option) combination.
	Hypotheticals []Hypothetical
	// Rows holds per-line current and candidate offers, in vendor order
	// (by line item ID).
	Rows []ComparisonRow
	// Currency is always nil with the current API schemas: no currency field
	// is documented. Consumers must render it as unknown.
	Currency *string
	// FetchedAt records when the response was retrieved; there is no
	// documented TTL, so staleness policy belongs to the consumer.
	FetchedAt time.Time
}

// PlanStatus mirrors the documented CommitmentPlan.status enum.
type PlanStatus string

const (
	PlanStatusNew         PlanStatus = "new"
	PlanStatusReviewed    PlanStatus = "reviewed"
	PlanStatusScheduled   PlanStatus = "scheduled"
	PlanStatusCompleted   PlanStatus = "completed"
	PlanStatusDraft       PlanStatus = "draft"
	PlanStatusNeedsReview PlanStatus = "needs_review"
	PlanStatusInProgress  PlanStatus = "in_progress"
	PlanStatusCancelled   PlanStatus = "cancelled" //nolint:misspell // vendor wire value
)

// Plan mirrors the subset of the documented CommitmentPlan schema that
// consumers display. The schema gives these numbers no description or unit;
// render them verbatim with their vendor field names and never convert them
// (no hourly-to-monthly scaling, no percent conversion of CommitmentCoverage).
type Plan struct {
	ID     string
	Status PlanStatus
	// IsCalculating is true while Archera recomputes the plan; consumers
	// show a non-final state instead of treating the figures as current.
	IsCalculating bool
	// FeeHourly is the vendor's fee_hourly, unit basis undocumented.
	FeeHourly *big.Rat
	// SavingsHourly is the vendor's savings_hourly, unit basis undocumented.
	SavingsHourly *big.Rat
	// CommitmentCoverage is the vendor's commitment_coverage; its scale
	// (0-1 or percent) is undocumented. It is native commitment coverage,
	// never insured coverage.
	CommitmentCoverage *big.Rat
	// MonthlySavings is the vendor's monthly_savings, basis undocumented.
	MonthlySavings *big.Rat
}

// ComparisonRequest selects the line items, target terms, and payment options
// for a comparison read. Empty slices follow the documented defaults: all
// selected line items, every distinct candidate term, no_upfront only.
type ComparisonRequest struct {
	PlanID         string
	LineItemIDs    []string
	ContractTerms  []string
	PaymentOptions []PaymentOption
}

// QuoteClient is the read-only seam implemented by the real HTTP client and
// by the test fake. It has no method that creates, updates, applies, or binds
// anything. When a consumer is unconfigured, no vendor request is made; a
// configured consumer calls it only on an explicit user action.
type QuoteClient interface {
	Comparison(ctx context.Context, req ComparisonRequest) (*Comparison, error)
	Plan(ctx context.Context, planID string) (*Plan, error)
}
