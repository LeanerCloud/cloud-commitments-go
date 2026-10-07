package exchange

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// ExchangeRecord is a lightweight record type for the auto exchange logic.
//
// It mirrors config.RIExchangeRecord but lives in pkg/exchange to avoid
// cross-module imports (pkg/ is a separate Go module from internal/).
//
//nolint:revive // stutters, but cloud-commitments-platform references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeRecord struct {
	ID                 string
	AccountID          string
	ExchangeID         string
	Region             string
	SourceRIIDs        []string
	SourceInstanceType string
	SourceCount        int
	TargetOfferingID   string
	TargetInstanceType string
	TargetCount        int
	PaymentDue         string
	Status             string
	ApprovalToken      string
	Error              string
	Mode               string
	// LadderRunID links this exchange record to the ladder run that created it.
	// Nil for standalone ri_exchange_reshape task records.
	// The database column ri_exchange_history.ladder_run_id was added in
	// migration 000080 and is the authoritative source for origin scoping.
	LadderRunID *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	ExpiresAt   *time.Time
}

// RIExchangeStore is the subset of store operations needed by RunAutoExchange.
type RIExchangeStore interface {
	SaveRIExchangeRecord(ctx context.Context, record *ExchangeRecord) error
	// CancelAllPendingExchanges cancels every pending record regardless of origin.
	// Kept for interface compatibility; RunAutoExchange now calls
	// CancelPendingExchangesByOrigin instead to avoid cross-origin contamination.
	CancelAllPendingExchanges(ctx context.Context) (int64, error)
	// CancelPendingExchangesByOrigin cancels only pending records whose origin
	// matches:
	//   - common.ExchangeOriginStandalone: cancels WHERE ladder_run_id IS NULL
	//   - common.ExchangeOriginLadder:     cancels WHERE ladder_run_id IS NOT NULL
	// This prevents the standalone task from wiping out ladder-linked pending
	// reshapes and vice versa. Implementations must validate the origin and
	// fail loud on an unknown value (money path).
	CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error)
	GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]ExchangeRecord, error)
	GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error)
	CompleteRIExchange(ctx context.Context, id string, exchangeID string) error
	// ReserveRIExchange atomically checks daily spend and saves a processing
	// record with the returned execution ceiling. It sets record.ID.
	ReserveRIExchange(ctx context.Context, record *ExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error)
	CompleteRIExchangeWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error
	FailRIExchange(ctx context.Context, id string, errorMsg string) error
}

// ExchangeClientInterface abstracts the Client for testability.
//
//nolint:revive // stutters, but cloud-commitments-platform references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeClientInterface interface {
	GetQuote(ctx context.Context, req ExchangeQuoteRequest) (*ExchangeQuoteSummary, error)
	Execute(ctx context.Context, req ExchangeExecuteRequest) (string, *ExchangeQuoteSummary, error)
}

// RIExchangeConfig holds the runtime configuration for auto exchange.
type RIExchangeConfig struct {
	Mode                     string
	UtilizationThreshold     float64
	MaxPaymentPerExchangeUSD float64
	MaxPaymentDailyUSD       float64
	LookbackDays             int
}

// LookupOfferingFunc looks up a target offering ID for a given instance type and RI metadata.
type LookupOfferingFunc func(ctx context.Context, instanceType, productDesc, tenancy, scope string, duration int64) (string, error)

// RunAutoExchangeParams holds all dependencies for RunAutoExchange.
type RunAutoExchangeParams struct {
	Store          RIExchangeStore
	ExchangeClient ExchangeClientInterface
	LookupOffering LookupOfferingFunc
	RIs            []RIInfo
	Utilization    []UtilizationInfo
	Config         RIExchangeConfig
	AccountID      string
	Region         string
	DashboardURL   string

	// RIMetadata maps RI ID to its metadata (product description, tenancy, scope, duration).
	RIMetadata map[string]RIMetadataInfo

	// LadderRunID links all exchange records created by this run to the ladder
	// run that originated them. Nil for the standalone ri_exchange_reshape task.
	// When set, RunAutoExchange calls CancelPendingExchangesByOrigin with
	// ladderScoped=true (cancels only ladder-linked pendings). When nil, it
	// cancels only standalone (ladder_run_id IS NULL) pendings.
	LadderRunID *string

	// DryRun skips all state mutations (cancellations, record saves, exchange
	// executions). Outcomes are returned with Simulated=true. No store write
	// is performed; mock.AssertExpectations will catch any accidental mutation
	// call in tests (fail-loud gate).
	DryRun bool
}

// RIMetadataInfo holds the offering metadata for a specific RI.
type RIMetadataInfo struct {
	ProductDescription string
	InstanceTenancy    string
	Scope              string
	Duration           int64
}

// AutoExchangeResult contains the outcome of an auto exchange run.
type AutoExchangeResult struct {
	Mode      string
	Completed []ExchangeOutcome
	Pending   []ExchangeOutcome
	Failed    []ExchangeOutcome
	Skipped   []SkippedRecommendation
}

// ExchangeOutcome captures the result of a single exchange attempt.
//
//nolint:revive // stutters, but providers/aws/ladder references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeOutcome struct {
	RecordID           string
	ApprovalToken      string
	SourceRIID         string
	SourceInstanceType string
	TargetInstanceType string
	TargetOfferingID   string
	TargetCount        int32
	PaymentDue         string
	ExchangeID         string
	UtilizationPct     float64
	Error              string
	// Simulated is true when RunAutoExchangeParams.DryRun was set. The outcome
	// was analyzed and quoted but no record was saved and no exchange was executed.
	Simulated bool
}

// SkippedRecommendation captures a recommendation that was not processed.
type SkippedRecommendation struct {
	SourceRIID         string
	SourceInstanceType string
	Reason             string
}

const staleProcessingThreshold = 15 * time.Minute

// RunAutoExchange orchestrates automated RI exchanges.
func RunAutoExchange(ctx context.Context, params RunAutoExchangeParams) (*AutoExchangeResult, error) {
	result := &AutoExchangeResult{Mode: params.Config.Mode}

	// 1. Cancel stale pending records with origin-scoped cancellation.
	// DryRun skips ALL mutations including cancellation (no state is changed).
	// Non-DryRun: cancel only pending records that share this run's origin so
	// the standalone task does not wipe out ladder-linked pendings and vice versa.
	// Race condition note: if a user clicks approve at 5h59m while this new run
	// fires and cancels pending records, the TransitionRIExchangeStatus atomic
	// WHERE clause prevents the exchange from executing (record already canceled
	// → returns nil → handler returns 409).
	if !params.DryRun {
		origin := common.ExchangeOriginStandalone
		if params.LadderRunID != nil {
			origin = common.ExchangeOriginLadder
		}
		canceled, err := params.Store.CancelPendingExchangesByOrigin(ctx, origin)
		if err != nil {
			logging.Warnf("failed to cancel pending exchanges: %v", err)
		} else if canceled > 0 {
			logging.Infof("canceled %d stale pending exchange records (origin=%s)", canceled, origin)
		}
	}

	// 2. Log warning for stale processing records
	stale, err := params.Store.GetStaleProcessingExchanges(ctx, staleProcessingThreshold)
	if err != nil {
		logging.Warnf("failed to check stale processing exchanges: %v", err)
	}
	for i := range stale {
		s := &stale[i]
		logging.Warnf("stale processing exchange: record_id=%s account_id=%s source_ri_ids=%v updated_at=%s",
			s.ID, s.AccountID, s.SourceRIIDs, s.UpdatedAt.Format(time.RFC3339))
	}

	// 3. Analyze reshaping
	recs := AnalyzeReshaping(params.RIs, params.Utilization, params.Config.UtilizationThreshold)
	if len(recs) == 0 {
		logging.Info("all RIs well-utilized, nothing to do")
		return result, nil
	}

	logging.Infof("found %d reshape recommendations", len(recs))

	perExchangeCap := new(big.Rat).SetFloat64(params.Config.MaxPaymentPerExchangeUSD)

	for i := range recs {
		rec := &recs[i]
		if processRecommendation(ctx, params, *rec, perExchangeCap, result) {
			// H4: processAutoExchange signaled halt because a ledger write failed
			// after money moved. Stop processing further recommendations so
			// subsequent exchanges don't bypass the daily cap.
			break
		}
	}

	return result, nil
}

// processRecommendation handles a single reshape recommendation: validates,
// quotes, and either creates a pending record (manual) or executes (auto).
// Returns true (halt) when processAutoExchange signals that a ledger write
// failed after money moved and no further exchanges should be attempted.
func processRecommendation(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation, perExchangeCap *big.Rat, result *AutoExchangeResult) bool {
	// Skip idle RIs with no target
	if rec.TargetInstanceType == "" {
		result.Skipped = append(result.Skipped, SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             "RI is idle (0% utilization) - no target instance type recommended",
		})
		return false
	}

	offeringID, skip := resolveOffering(ctx, params, rec)
	if skip != nil {
		result.Skipped = append(result.Skipped, *skip)
		return false
	}

	quote, skip := getValidatedQuote(ctx, params, rec, offeringID, perExchangeCap)
	if skip != nil {
		result.Skipped = append(result.Skipped, *skip)
		return false
	}

	paymentDueStr := quote.PaymentDueUSD.FloatString(6)

	if params.Config.Mode == "manual" {
		outcome := processManualExchange(ctx, params, rec, offeringID, paymentDueStr)
		if outcome.Error != "" {
			result.Failed = append(result.Failed, outcome)
		} else {
			result.Pending = append(result.Pending, outcome)
		}
		return false
	}

	outcome, halt := processAutoExchange(ctx, params, rec, offeringID, paymentDueStr, perExchangeCap)
	if outcome.Error != "" {
		result.Failed = append(result.Failed, outcome)
	} else {
		result.Completed = append(result.Completed, outcome)
	}
	return halt
}

// resolveOffering looks up RI metadata and finds the target offering ID.
// Returns the offering ID on success, or a SkippedRecommendation on failure.
func resolveOffering(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation) (string, *SkippedRecommendation) {
	meta, ok := params.RIMetadata[rec.SourceRIID]
	if !ok {
		return "", &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             "missing RI metadata",
		}
	}

	offeringID, err := params.LookupOffering(ctx, rec.TargetInstanceType, meta.ProductDescription, meta.InstanceTenancy, meta.Scope, meta.Duration)
	if err != nil {
		logging.Warnf("offering lookup failed for %s -> %s: %v", rec.SourceRIID, rec.TargetInstanceType, err)
		return "", &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             fmt.Sprintf("no matching offering found: %v", err),
		}
	}

	return offeringID, nil
}

// getValidatedQuote fetches and validates an exchange quote. The returned
// quote is valid, carries a USD PaymentDue, and is within the per-exchange cap.
// Returns a SkippedRecommendation otherwise.
func getValidatedQuote(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation, offeringID string, perExchangeCap *big.Rat) (*ExchangeQuoteSummary, *SkippedRecommendation) {
	quote, err := params.ExchangeClient.GetQuote(ctx, ExchangeQuoteRequest{
		Region:           params.Region,
		ReservedIDs:      []string{rec.SourceRIID},
		TargetOfferingID: offeringID,
		TargetCount:      rec.TargetCount,
	})
	if err != nil {
		logging.Warnf("quote failed for %s: %v", rec.SourceRIID, err)
		return nil, &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             fmt.Sprintf("quote failed: %v", err),
		}
	}

	if !quote.IsValidExchange {
		return nil, &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             fmt.Sprintf("invalid exchange: %s", quote.ValidationFailureReason),
		}
	}

	paymentDue, err := requireUSDPaymentDue(quote)
	if err != nil {
		return nil, &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason:             fmt.Sprintf("cannot enforce the per-exchange cap: %v", err),
		}
	}

	if paymentDue.Cmp(perExchangeCap) > 0 {
		return nil, &SkippedRecommendation{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			Reason: fmt.Sprintf("exceeds per-exchange cap: payment $%s > cap $%.2f",
				paymentDue.FloatString(2), params.Config.MaxPaymentPerExchangeUSD),
		}
	}

	return quote, nil
}

func processManualExchange(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation, offeringID, paymentDueStr string) ExchangeOutcome {
	// DryRun: return a simulated pending outcome without any record save or
	// token generation (tokens are actionable money instruments — never issue
	// live tokens in a dry run).
	if params.DryRun {
		return ExchangeOutcome{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			TargetInstanceType: rec.TargetInstanceType,
			TargetOfferingID:   offeringID,
			TargetCount:        rec.TargetCount,
			PaymentDue:         paymentDueStr,
			UtilizationPct:     rec.UtilizationPercent,
			Simulated:          true,
		}
	}

	token, err := common.GenerateApprovalToken()
	if err != nil {
		logging.Errorf("failed to generate approval token for %s: %v", rec.SourceRIID, err)
		errMsg := fmt.Sprintf("failed to generate approval token: %v", err)
		// Persist a failed record so an operator auditing the DB sees this
		// failure, mirroring the auto-mode failure paths in
		// processAutoExchange. crypto/rand failures are rare in practice
		// but still merit an audit trail.
		saveFailedRecord(ctx, params, rec, offeringID, paymentDueStr, errMsg, ExchangeModeManual)
		return ExchangeOutcome{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			TargetInstanceType: rec.TargetInstanceType,
			TargetOfferingID:   offeringID,
			TargetCount:        rec.TargetCount,
			PaymentDue:         paymentDueStr,
			UtilizationPct:     rec.UtilizationPercent,
			Error:              errMsg,
		}
	}
	// 24h is a safety net; the email says "approve within 6 hours" because
	// CancelPendingExchangesByOrigin at the next run start (every 6h) will cancel
	// this record. The 24h expiry catches edge cases where the scheduled run
	// is delayed or disabled.
	expiresAt := time.Now().Add(24 * time.Hour)

	record := &ExchangeRecord{
		AccountID:          params.AccountID,
		Region:             params.Region,
		SourceRIIDs:        []string{rec.SourceRIID},
		SourceInstanceType: rec.SourceInstanceType,
		SourceCount:        int(rec.SourceCount),
		TargetOfferingID:   offeringID,
		TargetInstanceType: rec.TargetInstanceType,
		TargetCount:        int(rec.TargetCount),
		PaymentDue:         paymentDueStr,
		Status:             "pending",
		ApprovalToken:      token,
		Mode:               string(ExchangeModeManual),
		ExpiresAt:          &expiresAt,
		LadderRunID:        params.LadderRunID,
	}

	if err := params.Store.SaveRIExchangeRecord(ctx, record); err != nil {
		logging.Errorf("failed to save pending exchange record for %s: %v", rec.SourceRIID, err)
		return ExchangeOutcome{
			SourceRIID:         rec.SourceRIID,
			SourceInstanceType: rec.SourceInstanceType,
			TargetInstanceType: rec.TargetInstanceType,
			TargetOfferingID:   offeringID,
			TargetCount:        rec.TargetCount,
			PaymentDue:         paymentDueStr,
			UtilizationPct:     rec.UtilizationPercent,
			Error:              fmt.Sprintf("failed to save record: %v", err),
		}
	}

	return ExchangeOutcome{
		RecordID:           record.ID,
		ApprovalToken:      token,
		SourceRIID:         rec.SourceRIID,
		SourceInstanceType: rec.SourceInstanceType,
		TargetInstanceType: rec.TargetInstanceType,
		TargetOfferingID:   offeringID,
		TargetCount:        rec.TargetCount,
		PaymentDue:         paymentDueStr,
		UtilizationPct:     rec.UtilizationPercent,
	}
}

// maxLedgerAttempts bounds settlement retries after money moves; persistent
// failure leaves the processing reservation in place and halts the run.
const maxLedgerAttempts = 3

func completeLedgerRecord(ctx context.Context, store RIExchangeStore, recordID, exchangeID, acceptedPaymentDue, sourceRIID string) error {
	var err error
	for attempt := 1; attempt <= maxLedgerAttempts; attempt++ {
		err = store.CompleteRIExchangeWithPayment(ctx, recordID, exchangeID, acceptedPaymentDue)
		if err == nil {
			return nil
		}
		if attempt < maxLedgerAttempts {
			logging.Warnf("exchange settlement retry %d/%d for %s after money moved: %v",
				attempt, maxLedgerAttempts, sourceRIID, err)
		}
	}
	return err
}

// floorCapUSD keeps a six-decimal reservation cap at or below its source.
func floorCapUSD(limit *big.Rat) string {
	scale := big.NewInt(1_000_000)
	units := new(big.Int).Div(new(big.Int).Mul(limit.Num(), scale), limit.Denom())
	return new(big.Rat).SetFrac(units, scale).FloatString(6)
}

// acceptedAmountFromQuote returns the payment amount confirmed by the fresh
// Execute quote. Execute refuses a re-quote with no PaymentDue, so a fresh
// quote without an amount can only come from a client that skipped that
// check; the initial quoted amount is then the honest ledger value (H3 fix).
func acceptedAmountFromQuote(freshQ *ExchangeQuoteSummary, fallback string) string {
	if freshQ != nil && freshQ.PaymentDueUSDStr != "" {
		return freshQ.PaymentDueUSDStr
	}
	return fallback
}

// processAutoExchange executes one auto exchange; halt=true means settlement
// failed after money moved and the processing reservation remains in place.
func processAutoExchange(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation, offeringID, paymentDueStr string, perExchangeCap *big.Rat) (ExchangeOutcome, bool) {
	outcome := ExchangeOutcome{
		SourceRIID:         rec.SourceRIID,
		SourceInstanceType: rec.SourceInstanceType,
		TargetInstanceType: rec.TargetInstanceType,
		TargetOfferingID:   offeringID,
		TargetCount:        rec.TargetCount,
		PaymentDue:         paymentDueStr,
		UtilizationPct:     rec.UtilizationPercent,
	}

	// DryRun: return a simulated completed outcome without executing the exchange
	// or saving any record. The daily cap is intentionally skipped so the
	// simulation reflects what would happen if the cap were not a factor.
	if params.DryRun {
		outcome.Simulated = true
		return outcome, false
	}

	// A direct caller can supply malformed payment text even though the normal
	// quote path formats it. Refuse it before creating a reservation.
	_, err := ParseDecimalRat(paymentDueStr)
	if err != nil {
		logging.Errorf("failed to parse payment due %q for %s: %v", paymentDueStr, rec.SourceRIID, err)
		outcome.Error = fmt.Sprintf("failed to parse payment due %q: %v", paymentDueStr, err)
		saveFailedRecord(ctx, params, rec, offeringID, paymentDueStr, outcome.Error, ExchangeModeAuto)
		return outcome, false
	}

	record := &ExchangeRecord{
		AccountID:          params.AccountID,
		Region:             params.Region,
		SourceRIIDs:        []string{rec.SourceRIID},
		SourceInstanceType: rec.SourceInstanceType,
		SourceCount:        int(rec.SourceCount),
		TargetOfferingID:   offeringID,
		TargetInstanceType: rec.TargetInstanceType,
		TargetCount:        int(rec.TargetCount),
		PaymentDue:         paymentDueStr,
		Status:             "processing",
		Mode:               string(ExchangeModeAuto),
		LadderRunID:        params.LadderRunID,
	}
	dailyCap := new(big.Rat).SetFloat64(params.Config.MaxPaymentDailyUSD)
	reservedUSD, err := params.Store.ReserveRIExchange(ctx, record, floorCapUSD(dailyCap), floorCapUSD(perExchangeCap))
	if err != nil {
		logging.Errorf("exchange reservation failed for %s: %v", rec.SourceRIID, err)
		outcome.Error = fmt.Sprintf("exchange reservation failed: %v", err)
		return outcome, false
	}
	outcome.RecordID = record.ID
	if record.ID == "" {
		outcome.Error = "exchange reservation returned no record ID"
		return outcome, true
	}
	effectiveCap, err := ParseDecimalRat(reservedUSD)
	if err != nil || effectiveCap.Sign() < 0 {
		outcome.Error = fmt.Sprintf("exchange reservation returned invalid ceiling %q", reservedUSD)
		return outcome, true
	}

	// Execute the exchange
	exchangeID, freshQ, execErr := params.ExchangeClient.Execute(ctx, ExchangeExecuteRequest{
		Region:           params.Region,
		ReservedIDs:      []string{rec.SourceRIID},
		TargetOfferingID: offeringID,
		TargetCount:      rec.TargetCount,
		MaxPaymentDueUSD: effectiveCap,
	})

	if execErr != nil {
		logging.Errorf("exchange execution failed for %s: %v", rec.SourceRIID, execErr)
		outcome.Error = execErr.Error()
		if failErr := params.Store.FailRIExchange(ctx, record.ID, outcome.Error); failErr != nil {
			logging.Errorf("failed to mark exchange reservation %s failed: %v", record.ID, failErr)
			return outcome, true
		}
		return outcome, false
	}

	// H3: persist the amount AWS actually accepted, not the stale pre-execution
	// quote. acceptedAmountFromQuote extracts PaymentDueUSDStr from the fresh
	// Execute quote; falls back to paymentDueStr when freshQ is nil (defensive).
	accepted := acceptedAmountFromQuote(freshQ, paymentDueStr)

	// Set ExchangeID now so it is present in the outcome even if the ledger
	// write fails below (callers and logs need it to correlate with AWS).
	outcome.ExchangeID = exchangeID
	outcome.PaymentDue = accepted

	saveErr := completeLedgerRecord(ctx, params.Store, record.ID, exchangeID, accepted, rec.SourceRIID)
	if saveErr != nil {
		logging.Errorf("all %d exchange settlement attempts failed for %s after money moved: %v; halting with reservation intact",
			maxLedgerAttempts, rec.SourceRIID, saveErr)
		outcome.Error = fmt.Sprintf("exchange settlement failed after exchange executed: %v", saveErr)
		return outcome, true // halt=true: stop processing further exchanges
	}

	return outcome, false
}

// Mode constrains the originating code path of an exchange record so
// `saveFailedRecord` (and any future caller) can't silently leak a typo into
// `ExchangeRecord.Mode`. The storage field stays `string` for serialization
// stability — this is a call-site discipline, not a schema change.
type Mode string

const (
	ExchangeModeAuto   Mode = "auto"
	ExchangeModeManual Mode = "manual"
)

// saveFailedRecord persists a failed exchange attempt for DB audit.
// `mode` distinguishes auto-mode failures from manual-mode failures so
// downstream filters/UI can split the two.
func saveFailedRecord(ctx context.Context, params RunAutoExchangeParams, rec ReshapeRecommendation, offeringID, paymentDueStr, errMsg string, mode Mode) {
	record := &ExchangeRecord{
		AccountID:          params.AccountID,
		Region:             params.Region,
		SourceRIIDs:        []string{rec.SourceRIID},
		SourceInstanceType: rec.SourceInstanceType,
		SourceCount:        int(rec.SourceCount),
		TargetOfferingID:   offeringID,
		TargetInstanceType: rec.TargetInstanceType,
		TargetCount:        int(rec.TargetCount),
		PaymentDue:         paymentDueStr,
		Status:             "failed",
		Error:              errMsg,
		Mode:               string(mode),
		LadderRunID:        params.LadderRunID,
	}
	if err := params.Store.SaveRIExchangeRecord(ctx, record); err != nil {
		logging.Errorf("failed to save failed exchange record for %s: %v", rec.SourceRIID, err)
	}
}
