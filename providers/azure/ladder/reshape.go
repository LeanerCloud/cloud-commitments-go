package ladder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
)

// exchangeClient is satisfied by *compute.Client.
type exchangeClient interface {
	ListExchangeableReservations(ctx context.Context) ([]compute.ExchangeableReservation, error)
	CalculateExchange(ctx context.Context, sources []compute.ExchangeableReservation, targets []compute.ExchangeTarget) (*compute.ExchangePreview, []compute.CompatibleOffering, error)
	ExecuteExchange(ctx context.Context, sessionID string) (*compute.ExchangeResult, error)
}

// WithExchange wires the reservation exchange client used by ReshapeBuffer.
// Exchanges still require the execution switch from WithPurchase.
func WithExchange(client exchangeClient) Option {
	return func(a *AzureLadder) error {
		if client == nil {
			return errors.New("WithExchange: exchange client must not be nil")
		}
		a.exchange = client
		return nil
	}
}

// dailySpend totals the exchange payments this process executed on the current
// UTC day. Azure exposes no history of past exchange payments, so spend from
// earlier processes or other callers is not visible here.
type dailySpend struct {
	day   string
	total float64
	mu    sync.Mutex
	// halted is set when an execute fails with an unknown outcome and stays set
	// for the life of the instance.
	halted bool
}

func (d *dailySpend) halt() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.halted = true
}

func (d *dailySpend) isHalted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.halted
}

func (d *dailySpend) spent(now time.Time) float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if day := now.UTC().Format(time.DateOnly); day != d.day {
		d.day, d.total = day, 0
	}
	return d.total
}

func (d *dailySpend) add(now time.Time, amount float64) {
	d.spent(now)
	d.mu.Lock()
	d.total += amount
	d.mu.Unlock()
}

type reshapeCaps struct {
	perExchange, daily float64
}

func validateReshapeConfig(cfg ladder.BufferReshapeConfig) (reshapeCaps, error) {
	perExchange, err := ladder.RequireCap("MaxPaymentPerExchangeUSD", cfg.MaxPaymentPerExchangeUSD)
	if err != nil {
		return reshapeCaps{}, err
	}
	daily, err := ladder.RequireCap("MaxPaymentDailyUSD", cfg.MaxPaymentDailyUSD)
	if err != nil {
		return reshapeCaps{}, err
	}
	if math.IsNaN(cfg.UtilizationThresholdPct) || cfg.UtilizationThresholdPct <= 0 || cfg.UtilizationThresholdPct > 100 {
		return reshapeCaps{}, fmt.Errorf("UtilizationThresholdPct must be in (0, 100], got %g", cfg.UtilizationThresholdPct)
	}
	if cfg.LookbackDays <= 0 {
		return reshapeCaps{}, fmt.Errorf("LookbackDays must be > 0, got %d", cfg.LookbackDays)
	}
	return reshapeCaps{perExchange: perExchange, daily: daily}, nil
}

var errExchangeHalted = errors.New("exchanges are halted: an earlier execution failed with an unknown outcome; reconcile in Azure and restart the process")

// errExchangeNewTerm refuses execution: every exchange target is a new
// reservation whose term starts when it is bought (go#28 decision C2).
var errExchangeNewTerm = errors.New("exchange refused: the target reservation starts a new Azure term; only dry runs are allowed (go#28 C2)")

// ReshapeBuffer shrinks each of the subscription's exchangeable VM reservations
// whose utilization is below the threshold to the quantity its utilization
// uses, one exchange per reservation. Every check runs before any client call:
// with the switch off nothing is touched. Any run that is not a dry run is
// refused before any client call, because each exchange target starts a new
// Azure term (C2); a dry run prices exchanges without executing them. Once C2
// is lifted, an exchange executes only when its priced payment is known and
// fits both the per-exchange cap and what is left
// of the daily cap; a refund (negative payment) always fits. A failed execution
// stops the run, since its outcome may be unknown, and halts further exchanges
// for the life of this instance. The daily cap is per AzureLadder instance and
// per process, not an account-wide limit: Azure exposes no history of past
// exchange payments to read it back from.
func (a *AzureLadder) ReshapeBuffer(ctx context.Context, scope ladder.Scope, cfg ladder.BufferReshapeConfig) (ladder.ReshapeSummary, error) {
	caps, err := a.checkReshapeAllowed(scope, cfg)
	if err != nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: %w", err)
	}

	a.reshapeMu.Lock()
	defer a.reshapeMu.Unlock()
	// A concurrent run may have halted the instance while this one waited.
	if a.dailySpend.isHalted() {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: %w", errExchangeHalted)
	}

	sources, err := a.exchange.ListExchangeableReservations(ctx)
	if err != nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: listing exchangeable reservations failed: %w", err)
	}
	utils, err := a.riUtil.GetRIUtilization(ctx, cfg.LookbackDays)
	if err != nil {
		return ladder.ReshapeSummary{}, fmt.Errorf("ReshapeBuffer: reservation utilization failed: %w", err)
	}
	pctByID := make(map[string]float64, len(utils))
	for i := range utils {
		pctByID[lastSegment(utils[i].ReservedInstanceID)] = utils[i].UtilizationPercent
	}

	var summary ladder.ReshapeSummary
	for i := range sources {
		src := sources[i]
		summary.Analyzed++
		target, reason := a.shrinkTarget(src, pctByID, cfg.UtilizationThresholdPct)
		if reason != "" {
			summary.Skipped++
			summary.Details = append(summary.Details, fmt.Sprintf("skipped: %s: %s", src.ReservationID, reason))
			continue
		}
		done, detail, err := a.exchangeOne(ctx, src, target, caps, cfg.DryRun)
		summary.Details = append(summary.Details, detail)
		switch {
		case err != nil:
			return summary, fmt.Errorf("ReshapeBuffer: %w", err)
		case done:
			summary.Reshaped++
		default:
			summary.Skipped++
		}
	}
	return summary, nil
}

func (a *AzureLadder) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// checkReshapeAllowed runs every check that must pass before any client call.
func (a *AzureLadder) checkReshapeAllowed(scope ladder.Scope, cfg ladder.BufferReshapeConfig) (reshapeCaps, error) {
	if !a.executionEnabled {
		return reshapeCaps{}, ErrLadderExecutionDisabled
	}
	if a.exchange == nil {
		return reshapeCaps{}, ErrWriteNotWired
	}
	if err := a.validateScope(scope); err != nil {
		return reshapeCaps{}, err
	}
	if a.dailySpend.isHalted() {
		return reshapeCaps{}, errExchangeHalted
	}
	caps, err := validateReshapeConfig(cfg)
	if err != nil {
		return reshapeCaps{}, err
	}
	if !cfg.DryRun {
		return reshapeCaps{}, errExchangeNewTerm
	}
	return caps, nil
}

// shrinkTarget returns the smaller reservation to exchange src for, or the
// reason src is left alone.
func (a *AzureLadder) shrinkTarget(src compute.ExchangeableReservation, pctByID map[string]float64, threshold float64) (target compute.ExchangeTarget, skipReason string) {
	if !strings.EqualFold(src.BillingScopeID, "/subscriptions/"+a.subscription) {
		return compute.ExchangeTarget{}, fmt.Sprintf("billing scope %q is not this subscription", src.BillingScopeID)
	}
	pct, ok := pctByID[lastSegment(src.ReservationID)]
	if !ok {
		return compute.ExchangeTarget{}, "no utilization data"
	}
	if pct >= threshold {
		return compute.ExchangeTarget{}, fmt.Sprintf("utilization %.1f%% is not below %.1f%%", pct, threshold)
	}
	if src.Region == "" || src.Term == "" {
		return compute.ExchangeTarget{}, "region or term unknown"
	}
	qty := max(int32(math.Ceil(float64(src.Quantity)*pct/100)), 1)
	if qty >= src.Quantity {
		return compute.ExchangeTarget{}, "no smaller quantity covers the used share"
	}
	return compute.ExchangeTarget{
		SKU:            src.SKU,
		Location:       src.Region,
		Term:           armreservations.ReservationTerm(src.Term),
		BillingScopeID: src.BillingScopeID,
		Quantity:       qty,
	}, ""
}

// exchangeOne prices one exchange and, if it fits the caps and this is not a
// dry run, executes it. done reports an executed exchange.
func (a *AzureLadder) exchangeOne(ctx context.Context, src compute.ExchangeableReservation, target compute.ExchangeTarget, caps reshapeCaps, dryRun bool) (done bool, detail string, err error) {
	label := fmt.Sprintf("%s (%s x%d) -> x%d", src.ReservationID, src.SKU, src.Quantity, target.Quantity)
	preview, _, err := a.exchange.CalculateExchange(ctx, []compute.ExchangeableReservation{src}, []compute.ExchangeTarget{target})
	if err != nil {
		return false, "failed: " + label, fmt.Errorf("pricing %s failed: %w", label, err)
	}
	now := a.clock()
	payment, skip := a.checkPayment(preview, caps, now)
	if skip != "" {
		return false, fmt.Sprintf("skipped: %s: %s", label, skip), nil
	}
	if dryRun {
		return false, fmt.Sprintf("dry run: %s, payment $%.2f", label, payment), nil
	}
	// Count the payment before executing: a failed call may still have been carried out.
	if payment > 0 {
		a.dailySpend.add(now, payment)
	}
	if _, err := a.exchange.ExecuteExchange(ctx, preview.SessionID); err != nil {
		a.dailySpend.halt()
		return false, "failed: " + label, fmt.Errorf("executing %s failed, outcome may be unknown, reconcile before retrying: %w", label, err)
	}
	return true, fmt.Sprintf("reshaped: %s, payment $%.2f, session %s", label, payment, preview.SessionID), nil
}

// checkPayment returns the priced payment, or why the exchange must not run.
func (a *AzureLadder) checkPayment(preview *compute.ExchangePreview, caps reshapeCaps, now time.Time) (payment float64, skipReason string) {
	if len(preview.PolicyErrors) > 0 {
		return 0, "policy errors: " + strings.Join(preview.PolicyErrors, "; ")
	}
	if preview.NetPayable == nil || math.IsNaN(*preview.NetPayable) || math.IsInf(*preview.NetPayable, 0) {
		return 0, "payment unknown"
	}
	payment = *preview.NetPayable
	if payment > caps.perExchange {
		return 0, fmt.Sprintf("payment $%.2f exceeds per-exchange cap $%.2f", payment, caps.perExchange)
	}
	if spent := a.dailySpend.spent(now); payment > 0 && spent+payment > caps.daily {
		return 0, fmt.Sprintf("payment $%.2f exceeds the remaining daily cap ($%.2f of $%.2f spent)", payment, spent, caps.daily)
	}
	return payment, ""
}
