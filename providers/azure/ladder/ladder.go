package ladder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

const (
	layerLookbackDays = 30
	layerHorizonDays  = 30
	savingsPlanOrders = "/savingsplanorders/"
)

// ErrBaselineBasisUnresolved is returned by GetUsageBaseline until the Azure
// usage series basis (residual or total) is decided; a wrong basis subtracts
// existing commitment twice or not at all.
var ErrBaselineBasisUnresolved = errors.New("azure usage baseline blocked: the basis decision (residual or total usage) recorded in the go#28 plan is not settled")

// ErrWriteNotWired is returned by ReshapeBuffer until go#28 slice C2 wires it.
var ErrWriteNotWired = errors.New("azure ladder write side not wired: ReshapeBuffer arrives with go#28 slice C2")

type commitmentLister interface {
	GetExistingCommitments(ctx context.Context) ([]common.Commitment, error)
}

type savingsPlanUtilizationSource interface {
	GetSavingsPlanUtilization(ctx context.Context, orderIDs []string, start, end time.Time) (map[string]float64, error)
}

type riUtilizationSource interface {
	GetRIUtilization(ctx context.Context, lookbackDays int) ([]common.RIUtilization, error)
}

// AzureLadder implements ladder.LadderCapability for one Azure subscription.
// The reservation layer covers VM reservations only.
type AzureLadder struct {
	reservations commitmentLister
	savingsPlans commitmentLister
	spUtil       savingsPlanUtilizationSource
	riUtil       riUtilizationSource
	spPurchase   commitmentPurchaser
	riPurchase   commitmentPurchaser

	subscription     string
	executionEnabled bool
}

var _ ladder.LadderCapability = (*AzureLadder)(nil)

// NewAzureLadder wires the existing Azure clients into a ladder capability for
// the given subscription; purchases stay off unless WithPurchase enables them.
func NewAzureLadder(subscriptionID string, reservations, savingsPlans commitmentLister, spUtil savingsPlanUtilizationSource, riUtil riUtilizationSource, opts ...Option) (*AzureLadder, error) {
	switch {
	case subscriptionID == "":
		return nil, errors.New("AzureLadder: subscription ID must not be empty")
	case reservations == nil:
		return nil, errors.New("AzureLadder: reservation lister must not be nil")
	case savingsPlans == nil:
		return nil, errors.New("AzureLadder: savings plan lister must not be nil")
	case spUtil == nil:
		return nil, errors.New("AzureLadder: savings plan utilization source must not be nil")
	case riUtil == nil:
		return nil, errors.New("AzureLadder: reservation utilization source must not be nil")
	}
	a := &AzureLadder{subscription: subscriptionID, reservations: reservations, savingsPlans: savingsPlans, spUtil: spUtil, riUtil: riUtil}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("AzureLadder: option must not be nil")
		}
		if err := opt(a); err != nil {
			return nil, fmt.Errorf("AzureLadder: %w", err)
		}
	}
	return a, nil
}

// Provider returns common.ProviderAzure.
func (a *AzureLadder) Provider() common.ProviderType {
	return common.ProviderAzure
}

// SupportedLayers returns the reservation layer (base and buffer merged) and
// the savings plan layer (flex).
func (a *AzureLadder) SupportedLayers() []ladder.LayerSpec {
	return []ladder.LayerSpec{
		{Type: ladder.LayerAzureReservation, Roles: []ladder.LayerRole{ladder.RoleBase, ladder.RoleBuffer}},
		{Type: ladder.LayerAzureSavingsPlan, Roles: []ladder.LayerRole{ladder.RoleFlex}},
	}
}

func (a *AzureLadder) validateScope(scope ladder.Scope) error {
	if scope.Provider != common.ProviderAzure || scope.AccountID != a.subscription {
		return fmt.Errorf("AzureLadder: scope %s/%s does not match subscription %s", scope.Provider, scope.AccountID, a.subscription)
	}
	return nil
}

// ListCommitments returns the subscription's VM reservations followed by its savings plans.
func (a *AzureLadder) ListCommitments(ctx context.Context, scope ladder.Scope) ([]common.Commitment, error) {
	if err := a.validateScope(scope); err != nil {
		return nil, err
	}
	ris, err := a.reservations.GetExistingCommitments(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListCommitments: reservation listing failed: %w", err)
	}
	sps, err := a.savingsPlans.GetExistingCommitments(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListCommitments: savings plan listing failed: %w", err)
	}
	return append(ris, sps...), nil
}

// GetLayerStates snapshots both layers. The savings plan amount is the hourly
// commitment Azure reports. The reservation inventory carries no price, so a
// subscription with an active reservation fails loud instead of reporting zero.
func (a *AzureLadder) GetLayerStates(ctx context.Context, scope ladder.Scope) (map[ladder.LayerType]ladder.LayerState, error) {
	if err := a.validateScope(scope); err != nil {
		return nil, err
	}
	ris, err := a.reservations.GetExistingCommitments(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetLayerStates: reservation listing failed: %w", err)
	}
	sps, err := a.savingsPlans.GetExistingCommitments(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetLayerStates: savings plan listing failed: %w", err)
	}
	horizon := time.Now().Add(layerHorizonDays * 24 * time.Hour)

	riState, err := a.reservationState(ctx, active(ris), horizon)
	if err != nil {
		return nil, err
	}
	spState, err := a.savingsPlanState(ctx, active(sps), horizon)
	if err != nil {
		return nil, err
	}
	return map[ladder.LayerType]ladder.LayerState{
		ladder.LayerAzureReservation: riState,
		ladder.LayerAzureSavingsPlan: spState,
	}, nil
}

// countsAsExisting includes payment-pending commitments: a just-purchased plan
// must count at once or the next run would buy it again.
func countsAsExisting(s common.CommitmentState) bool {
	return s == common.CommitmentStateActive || s == common.CommitmentStatePaymentPending
}

func active(cs []common.Commitment) []common.Commitment {
	var out []common.Commitment
	for i := range cs {
		if countsAsExisting(cs[i].State) {
			out = append(out, cs[i])
		}
	}
	return out
}

// sumCost totals the hourly USD cost and the share expiring by horizon. Cost
// that is unknown or not in USD fails loud: the engine compares it to USD usage.
func sumCost(kind string, cs []common.Commitment, horizon time.Time) (existing, expiring float64, err error) {
	for i := range cs {
		c := &cs[i]
		if !c.CostKnown {
			return 0, 0, fmt.Errorf("GetLayerStates: %s %s has no known price; the layer cost cannot be measured", kind, c.CommitmentID)
		}
		if c.CostCurrency != "USD" {
			return 0, 0, fmt.Errorf("GetLayerStates: %s %s is priced in %q; only USD is supported", kind, c.CommitmentID, c.CostCurrency)
		}
		existing += c.Cost
		if !c.EndDate.IsZero() && !c.EndDate.After(horizon) {
			expiring += c.Cost
		}
	}
	return existing, expiring, nil
}

type utilizationSample struct {
	id     string
	pct    float64
	weight float64
}

// weightedUtilization averages samples by weight. Samples that match no
// commitment are ignored; if samples exist but none match, the ids cannot be
// joined and that is an error, not an unmeasured layer.
func weightedUtilization(kind string, cs []common.Commitment, samples []utilizationSample, weightOf func(*common.Commitment, utilizationSample) float64) (*float64, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	own := map[string]*common.Commitment{}
	for i := range cs {
		own[lastSegment(cs[i].CommitmentID)] = &cs[i]
	}
	var sum, weight float64
	matched := false
	for _, s := range samples {
		c, ok := own[lastSegment(s.id)]
		if !ok {
			continue
		}
		matched = true
		w := weightOf(c, s)
		sum += s.pct * w
		weight += w
	}
	if !matched {
		return nil, fmt.Errorf("GetLayerStates: %s utilization returned %d entries but none match this subscription's commitments", kind, len(samples))
	}
	if weight == 0 {
		return nil, nil
	}
	m := sum / weight
	return &m, nil
}

func lastSegment(id string) string {
	return strings.ToLower(id[strings.LastIndex(id, "/")+1:])
}

func (a *AzureLadder) reservationState(ctx context.Context, ris []common.Commitment, horizon time.Time) (ladder.LayerState, error) {
	existing, expiring, err := sumCost("reservation", ris, horizon)
	if err != nil {
		return ladder.LayerState{}, err
	}
	state := ladder.LayerState{Layer: ladder.LayerAzureReservation, ExistingUSDPerHour: &existing, ExpiringUSDPerHour: &expiring}
	if len(ris) == 0 {
		return state, nil
	}
	utils, err := a.riUtil.GetRIUtilization(ctx, layerLookbackDays)
	if err != nil {
		return ladder.LayerState{}, fmt.Errorf("GetLayerStates: reservation utilization failed: %w", err)
	}
	samples := make([]utilizationSample, 0, len(utils))
	for i := range utils {
		samples = append(samples, utilizationSample{id: utils[i].ReservedInstanceID, pct: utils[i].UtilizationPercent, weight: utils[i].PurchasedHours})
	}
	state.UtilizationPct, err = weightedUtilization("reservation", ris, samples, func(_ *common.Commitment, s utilizationSample) float64 { return s.weight })
	if err != nil {
		return ladder.LayerState{}, err
	}
	return state, nil
}

func (a *AzureLadder) savingsPlanState(ctx context.Context, sps []common.Commitment, horizon time.Time) (ladder.LayerState, error) {
	existing, expiring, err := sumCost("savings plan", sps, horizon)
	if err != nil {
		return ladder.LayerState{}, err
	}
	state := ladder.LayerState{Layer: ladder.LayerAzureSavingsPlan, ExistingUSDPerHour: &existing, ExpiringUSDPerHour: &expiring}
	if len(sps) == 0 {
		return state, nil
	}
	var orderIDs []string
	seen := map[string]bool{}
	for i := range sps {
		orderID, oerr := savingsPlanOrderID(sps[i].CommitmentID)
		if oerr != nil {
			return ladder.LayerState{}, oerr
		}
		if !seen[orderID] {
			seen[orderID] = true
			orderIDs = append(orderIDs, orderID)
		}
	}
	end := time.Now().UTC()
	utils, err := a.spUtil.GetSavingsPlanUtilization(ctx, orderIDs, end.AddDate(0, 0, -layerLookbackDays), end)
	if err != nil {
		return ladder.LayerState{}, fmt.Errorf("GetLayerStates: savings plan utilization failed: %w", err)
	}
	samples := make([]utilizationSample, 0, len(utils))
	for id, pct := range utils {
		samples = append(samples, utilizationSample{id: id, pct: pct})
	}
	state.UtilizationPct, err = weightedUtilization("savings plan", sps, samples, func(c *common.Commitment, _ utilizationSample) float64 { return c.Cost })
	if err != nil {
		return ladder.LayerState{}, err
	}
	return state, nil
}

func savingsPlanOrderID(planID string) (string, error) {
	_, rest, ok := strings.Cut(strings.ToLower(planID), savingsPlanOrders)
	if !ok {
		return "", fmt.Errorf("GetLayerStates: savings plan id %q has no savingsPlanOrders segment", planID)
	}
	start := len(planID) - len(rest)
	order, _, _ := strings.Cut(planID[start:], "/")
	if order == "" {
		return "", fmt.Errorf("GetLayerStates: savings plan id %q has an empty order id", planID)
	}
	return order, nil
}

// GetUsageBaseline is blocked on the basis decision; it never computes a baseline.
func (a *AzureLadder) GetUsageBaseline(context.Context, ladder.Scope, int, float64) (ladder.UsageBaseline, error) {
	return ladder.UsageBaseline{}, ErrBaselineBasisUnresolved
}

// ReshapeBuffer is not wired until slice C2.
func (a *AzureLadder) ReshapeBuffer(context.Context, ladder.Scope, ladder.BufferReshapeConfig) (ladder.ReshapeSummary, error) {
	return ladder.ReshapeSummary{}, ErrWriteNotWired
}
