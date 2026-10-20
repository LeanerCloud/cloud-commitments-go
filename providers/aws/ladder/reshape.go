package ladder

import (
	"context"
	"fmt"
	"math"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ReshapeBuffer runs the automated RI exchange flow over the buffer layer
// (convertible RIs), delegating to the injected exchangeRunner. AWSLadder
// only maps the configuration and the outcome; the runner owns the exchange
// store, quote/execute client, offering lookup, and RI/utilization inventory
// (the same wiring internal/server.executeRIExchangeReshape performs).
//
// Origin-scoped cancellation (gap G10, issue #1348): exchange.RunAutoExchange
// scopes its pending-cancellation by origin — standalone runs
// (LadderRunID=nil) cancel only ladder_run_id IS NULL pendings, and ladder
// runs (LadderRunID set) cancel only ladder_run_id IS NOT NULL pendings. This
// method forwards cfg.LadderRunID through the exchangeRunner seam so a ladder
// reshape scopes correctly. NOTE: the standalone scoping is active today; the
// ladder-side scoping only takes effect once a concrete exchangeRunner is
// wired (L16) — no production caller wires the write side yet, so ReshapeBuffer
// fails loud with errWriteNotWired until then. The seam REQUIRES LadderRunID so
// the L16 runner cannot drop it and reintroduce the cross-origin bug.
//
// DryRun is forwarded through the seam (cfg.DryRun -> the runner's dryRun
// argument -> exchange.RunAutoExchangeParams.DryRun, which skips every
// mutation and returns Simulated outcomes). It is honored by the concrete
// runner wired in L16; today no production runner is wired, so a dry-run
// ReshapeBuffer call reaches only a test double or fails loud with
// errWriteNotWired.
//
// Config mapping (BufferReshapeConfig -> exchange.RIExchangeConfig):
//
//   - MaxPaymentPerExchangeUSD / MaxPaymentDailyUSD: both are required. A nil
//     cap is rejected loudly (Mode is always auto, so an omitted cap must
//     never mean unlimited spend), and values must be finite and > 0 — zero
//     is rejected because RunAutoExchange treats the cap as a skip threshold
//     and a zero cap would silently block every exchange (almost certainly a
//     config bug, not an intent).
//   - UtilizationThresholdPct must be in (0, 100]; LookbackDays must be > 0.
//   - Mode is always exchange.ExchangeModeAuto: exchanges execute immediately,
//     subject to the per-exchange and daily caps.
//
// Outcome mapping (exchange.AutoExchangeResult -> ladder.ReshapeSummary):
//
//   - Analyzed = Completed + Pending + Failed + Skipped: the number of reshape
//     recommendations processed. NOTE: this is not the total RI inventory size
//     (the thin runner seam does not expose it); it counts the commitments the
//     exchange analysis flagged and processed.
//   - Reshaped = Completed only (exchanges actually executed).
//   - Skipped  = Skipped only: below the utilization threshold, no matching
//     offering, invalid quote, or over the PER-EXCHANGE cap. A DAILY-cap stop
//     is NOT in this bucket: pkg/exchange classifies it as Failed
//     (saveFailedRecord + result.Failed), so a routine daily-cap policy stop
//     currently surfaces from this method as "N of M failed" plus a non-nil
//     error. Upstream reclassification of daily-cap stops as skips is
//     tracked in #1348.
//     Failed attempts are never counted as "skipped"; they surface in
//     Details AND as a non-nil error (money-path failures must never be
//     silently absorbed into a success-looking summary).
//
// Partial failures: when the runner reports failed exchange attempts, the
// populated summary is returned TOGETHER with a non-nil error so callers get
// both the audit detail and a loud failure signal.
func (a *AWSLadder) ReshapeBuffer(ctx context.Context, scope ladder.Scope, cfg ladder.BufferReshapeConfig) (ladder.ReshapeSummary, error) {
	if a.exchange == nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: %w", errWriteNotWired)
	}
	if err := a.validateScope(scope); err != nil {
		return ladder.ReshapeSummary{}, err
	}
	runCfg, err := buildRIExchangeConfig(cfg)
	if err != nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: %w", err)
	}

	// Forward LadderRunID and DryRun through the seam so the concrete runner
	// (L16) scopes cancellation to this origin and honors dry-run. See the
	// godoc and the exchangeRunner interface for why these are explicit params.
	result, err := a.exchange.RunAutoExchange(ctx, runCfg, cfg.LadderRunID, cfg.DryRun)
	if err != nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: auto exchange run failed: %w", err)
	}
	if result == nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: exchange runner returned a nil result without an error (runner contract violation)")
	}

	return summarizeExchangeResult(result)
}

// buildRIExchangeConfig validates cfg at the boundary and maps it to the
// exchange package's runtime configuration. See ReshapeBuffer's godoc for the
// full mapping rationale.
func buildRIExchangeConfig(cfg ladder.BufferReshapeConfig) (exchange.RIExchangeConfig, error) {
	perExchangeCap, err := requireCap("MaxPaymentPerExchangeUSD", cfg.MaxPaymentPerExchangeUSD)
	if err != nil {
		return exchange.RIExchangeConfig{}, err
	}
	dailyCap, err := requireCap("MaxPaymentDailyUSD", cfg.MaxPaymentDailyUSD)
	if err != nil {
		return exchange.RIExchangeConfig{}, err
	}
	if math.IsNaN(cfg.UtilizationThresholdPct) || cfg.UtilizationThresholdPct <= 0 || cfg.UtilizationThresholdPct > 100 {
		return exchange.RIExchangeConfig{}, fmt.Errorf(
			"UtilizationThresholdPct must be in (0, 100], got %g", cfg.UtilizationThresholdPct)
	}
	if cfg.LookbackDays <= 0 {
		return exchange.RIExchangeConfig{}, fmt.Errorf("LookbackDays must be > 0, got %d", cfg.LookbackDays)
	}

	// Mode is always auto. DryRun is not folded into RIExchangeConfig; it is
	// forwarded as an explicit seam argument so the runner routes it to
	// exchange.RunAutoExchangeParams.DryRun (which simulates without mutating,
	// regardless of Mode). See the ReshapeBuffer godoc.
	return exchange.RIExchangeConfig{
		Mode:                     string(exchange.ExchangeModeAuto),
		UtilizationThreshold:     cfg.UtilizationThresholdPct,
		MaxPaymentPerExchangeUSD: perExchangeCap,
		MaxPaymentDailyUSD:       dailyCap,
		LookbackDays:             cfg.LookbackDays,
	}, nil
}

// requireCap validates a required money cap and returns it as the float64 the
// exchange config requires. nil is rejected: the caller must set an explicit
// cap, so an omitted field can never mean unlimited spend. Values must be
// finite and > 0 (a zero cap would block every exchange).
func requireCap(name string, capUSD *float64) (float64, error) {
	if capUSD == nil {
		return 0, fmt.Errorf("%s is required: set an explicit cap (a nil cap is not treated as unlimited)", name)
	}
	v := *capUSD
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s must be finite, got %g", name, v)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be > 0 (a zero cap would block every exchange), got %g", name, v)
	}
	return v, nil
}

// summarizeExchangeResult maps the runner outcome to a ReshapeSummary.
// Index-based loops avoid copying the large outcome structs (rangeValCopy).
func summarizeExchangeResult(result *exchange.AutoExchangeResult) (ladder.ReshapeSummary, error) {
	summary := ladder.ReshapeSummary{
		Analyzed: len(result.Completed) + len(result.Pending) + len(result.Failed) + len(result.Skipped),
		Reshaped: len(result.Completed),
		Skipped:  len(result.Skipped),
		Details: make([]string, 0,
			len(result.Completed)+len(result.Pending)+len(result.Failed)+len(result.Skipped)),
	}

	unknown := 0
	firstUnknown := -1
	for i := range result.Completed {
		o := &result.Completed[i]
		summary.Details = append(summary.Details, fmt.Sprintf(
			"reshaped: %s (%s) -> %s x%d, payment $%s, exchange %s",
			o.SourceRIID, o.SourceInstanceType, o.TargetInstanceType, o.TargetCount, o.PaymentDue, o.ExchangeID))
	}
	for i := range result.Pending {
		o := &result.Pending[i]
		summary.Details = append(summary.Details, fmt.Sprintf(
			"pending approval (not executed): %s (%s) -> %s x%d, payment $%s",
			o.SourceRIID, o.SourceInstanceType, o.TargetInstanceType, o.TargetCount, o.PaymentDue))
	}
	for i := range result.Failed {
		o := &result.Failed[i]
		label := "failed"
		if o.OutcomeUnknown {
			unknown++
			if firstUnknown < 0 {
				firstUnknown = i
			}
			label = "outcome unknown, reconcile before retrying"
		}
		summary.Details = append(summary.Details, fmt.Sprintf(
			"%s: %s (%s) -> %s: %s",
			label, o.SourceRIID, o.SourceInstanceType, o.TargetInstanceType, o.Error))
	}
	for i := range result.Skipped {
		s := &result.Skipped[i]
		summary.Details = append(summary.Details, fmt.Sprintf(
			"skipped: %s (%s): %s", s.SourceRIID, s.SourceInstanceType, s.Reason))
	}

	if len(result.Failed) > 0 {
		// Denominate by actual exchange ATTEMPTS (completed + failed), not
		// summary.Analyzed: pending and skipped items were never attempted,
		// and an inflated denominator would misread during an incident.
		attempts := len(result.Completed) + len(result.Failed)
		const format = "ReshapeBuffer: %d of %d exchange attempt(s) failed (first: %s: %s)%s; see summary details for the full list"
		if unknown == 0 {
			first := &result.Failed[0]
			return summary, fmt.Errorf(format, len(result.Failed), attempts, first.SourceRIID, first.Error, "")
		}
		// The unknown entry is the one needing reconciliation, so name it
		// and wrap the sentinel for errors.Is.
		first := &result.Failed[firstUnknown]
		return summary, fmt.Errorf(format+": %w", len(result.Failed), attempts, first.SourceRIID, first.Error,
			fmt.Sprintf("; %d with unknown outcome", unknown), common.ErrOutcomeUnknown)
	}
	return summary, nil
}
