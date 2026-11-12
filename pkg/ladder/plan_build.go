package ladder

import (
	"fmt"
	"math/big"
	"slices"
	"time"
)

// PlanInput carries the engine outputs BuildPlan assembles into a LadderPlan.
// BuildPlan performs no I/O and never calls time.Now.
//
// Now must be the exact run clock the caller passed to BuildTranches: a
// tranche is "immediate" when its FireAfter equals Now.
type PlanInput struct {
	// Target is the on-demand-equivalent USD/hr coverage target. nil means it
	// could not be computed; the plan's gap is then nil too.
	Target *big.Rat
	// Result is the Allocate output the tranches were built from.
	Result *AllocateResult
	// Tranches is the BuildTranches output for Result.Allocations.
	Tranches *TrancheResult
	Scope    Scope
	Now      time.Time
	Baseline UsageBaseline
}

// BuildPlan assembles the validated LadderPlan for one scope run from the
// Allocate and BuildTranches outputs.
//
// Each immediate tranche (FireAfter equals Now) becomes one ActionPurchase
// whose amount is that tranche's own commitment USD/hr step, not the whole
// Allocation.GapUSDPerHour (the sum over every ramp step). The amount is
// pre-rounding: the purchase boundary still rounds it down to the provider's
// precision. Later tranches are scheduled rows persisted by the caller and are
// not actions. Tranches must be a fresh BuildTranches output: Status is not
// consulted, so a fired tranche whose FireAfter equals Now would look immediate.
// An allocation with no immediate tranche yields a hold that
// states when its first tranche fires, so an approver never reads "nothing"
// when a purchase is scheduled.
//
// Actions are ordered purchases, reshapes, the Allocate holds, then the
// delayed-ramp holds, so Explain output is deterministic.
//
// ExistingUSDPerHour is Result.NetExistingUSDPerHour (net of expiring).
// GapUSDPerHour is Target minus Existing, signed (negative means over target);
// it is a display figure, not Allocate's purchase sizing rule.
func BuildPlan(in *PlanInput) (*LadderPlan, error) {
	if err := validatePlanInput(in); err != nil {
		return nil, err
	}
	allocs, err := allocationsByLayer(in.Result.Allocations)
	if err != nil {
		return nil, err
	}
	purchases, firstFire, err := classifyTranches(in.Now, in.Tranches.Tranches, allocs)
	if err != nil {
		return nil, err
	}

	actions := slices.Clone(purchases)
	actions = append(actions, in.Result.Reshapes...)
	actions = append(actions, in.Result.Holds...)
	for _, a := range in.Result.Allocations {
		if !hasPurchase(purchases, a.Layer) {
			actions = append(actions, delayedHold(a, firstFire[a.Layer]))
		}
	}

	plan := &LadderPlan{
		Scope:              in.Scope,
		GeneratedAt:        in.Now,
		ExistingUSDPerHour: new(big.Rat).Set(in.Result.NetExistingUSDPerHour),
		Actions:            actions,
		Baseline:           in.Baseline,
	}
	if in.Target != nil {
		plan.TargetUSDPerHour = new(big.Rat).Set(in.Target)
		plan.GapUSDPerHour = new(big.Rat).Sub(in.Target, plan.ExistingUSDPerHour)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("built plan is invalid: %w", err)
	}
	return plan, nil
}

func validatePlanInput(in *PlanInput) error {
	switch {
	case in == nil:
		return fmt.Errorf("plan input must not be nil")
	case in.Now.IsZero():
		return fmt.Errorf("now must not be zero (inject the run wall-clock time)")
	case in.Result == nil:
		return fmt.Errorf("allocate result must not be nil")
	case in.Result.NetExistingUSDPerHour == nil:
		return fmt.Errorf("allocate result has no net existing commitment (Allocate always sets it)")
	case in.Tranches == nil:
		return fmt.Errorf("tranche result must not be nil")
	}
	return nil
}

func allocationsByLayer(allocs []Allocation) (map[LayerType]Allocation, error) {
	byLayer := make(map[LayerType]Allocation, len(allocs))
	for _, a := range allocs {
		if _, dup := byLayer[a.Layer]; dup {
			return nil, fmt.Errorf("duplicate allocation for layer %s", a.Layer)
		}
		byLayer[a.Layer] = a
	}
	return byLayer, nil
}

// classifyTranches turns each immediate tranche into a purchase and records
// each layer's earliest later fire time. Orphan, overdue and bad-amount
// tranches, and a second immediate tranche on a layer, are errors.
func classifyTranches(now time.Time, tranches []Tranche, allocs map[LayerType]Allocation) ([]PlannedAction, map[LayerType]time.Time, error) {
	var purchases []PlannedAction
	firstFire := make(map[LayerType]time.Time)
	for i := range tranches {
		tr := &tranches[i]
		alloc, ok := allocs[tr.Layer]
		if !ok {
			return nil, nil, fmt.Errorf("tranche %q: no allocation for layer %s", tr.ID, tr.Layer)
		}
		switch {
		case tr.FireAfter.Before(now):
			return nil, nil, fmt.Errorf("tranche %q is overdue: fire_after %s is before now %s",
				tr.ID, tr.FireAfter.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
		case tr.FireAfter.After(now):
			if first, seen := firstFire[tr.Layer]; !seen || tr.FireAfter.Before(first) {
				firstFire[tr.Layer] = tr.FireAfter
			}
			continue
		}
		if hasPurchase(purchases, tr.Layer) {
			return nil, nil, fmt.Errorf("layer %s has two immediate tranches", tr.Layer)
		}
		amount, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
		if !ok || amount.Sign() <= 0 {
			return nil, nil, fmt.Errorf("tranche %q: amount %q is not a positive rational", tr.ID, tr.AmountUSDPerHour)
		}
		purchases = append(purchases, PlannedAction{
			Action:           ActionPurchase,
			Layer:            tr.Layer,
			AmountUSDPerHour: amount,
			Term:             tr.Term,
			PaymentOption:    tr.PaymentOption,
			Rationale:        alloc.Rationale,
			DataSources:      slices.Clone(alloc.DataSources),
		})
	}
	return purchases, firstFire, nil
}

func hasPurchase(purchases []PlannedAction, layer LayerType) bool {
	return slices.ContainsFunc(purchases, func(p PlannedAction) bool { return p.Layer == layer })
}

func delayedHold(a Allocation, first time.Time) PlannedAction {
	when := "no tranche is scheduled for this layer"
	if !first.IsZero() {
		when = "first purchase is scheduled for " + first.UTC().Format(time.RFC3339)
	}
	return PlannedAction{
		Action:      ActionHold,
		Layer:       a.Layer,
		Rationale:   fmt.Sprintf("%s; no immediate purchase: %s", a.Rationale, when),
		DataSources: slices.Clone(a.DataSources),
	}
}
