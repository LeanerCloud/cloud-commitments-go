package insurance

import "math/big"

type RecommendationIdentity struct {
	HourlyCommitment *big.Rat

	Provider, SourceAccountID, Service, PlanType, OfferingID string

	Region, TopLevelRegion, InstanceFamily, Payment string
	CloudAccountID                                  string
	NativeDurationSeconds                           int64
}

type LineItemIdentity struct {
	DurationSeconds, SelectedCommitment                                *big.Rat
	Type, PlanType, Region, InstanceFamily, Payment                    string
	ID, PlanID, OfferID, Provider, SourceAccountID, ProviderOfferingID string
}

type ComparisonIdentity struct {
	Region                *string
	CurrentSelectedAmount *big.Rat

	LineItemID, CurrentOfferID, Provider, CurrentType, CurrentPayment string

	Candidates []CandidateIdentity
	IsCurrent  bool
}

type MatchStatus string

const (
	MatchUnavailable   MatchStatus = "unavailable"
	ResourceAssociated MatchStatus = "resource_associated"
)

// Reason names why a match, candidate, amount or economic status holds.
type Reason string

const (
	ReasonIdentityUnproven           Reason = "identity_unproven"
	ReasonAmbiguousLineItems         Reason = "ambiguous_line_items"
	ReasonComparisonBasisUnproven    Reason = "comparison_basis_unproven"
	ReasonCandidateBasisUnproven     Reason = "candidate_basis_unproven"
	ReasonStoredDecimalOnly          Reason = "stored_decimal_only"
	ReasonUnavailableSourcePrecision Reason = "unavailable_source_precision"
)

type RecommendationMatch struct {
	Status                                   MatchStatus
	LineItemID                               string
	Reason, AmountProvenance, EconomicStatus Reason
	Candidates                               []CandidateAssociation
}

// SelectStatus says how many line items share the recommendation's resource identity.
type SelectStatus string

const (
	SelectOne       SelectStatus = "selected"
	SelectNone      SelectStatus = "none"
	SelectAmbiguous SelectStatus = "ambiguous"
)

type CandidateIdentity struct {
	SelectedAmount                   *big.Rat
	OfferID, Provider, Type, Payment string
}

// CandidateStatus classifies a vendor candidate against the stored recommendation.
type CandidateStatus string

const (
	CandidateVendorAlternative CandidateStatus = "vendor_alternative"
	CandidateUnavailable       CandidateStatus = "unavailable"
)

type CandidateAssociation struct {
	OfferID              string
	Status               CandidateStatus
	Reason               Reason
	NativeIdentityStatus CandidateStatus
}
