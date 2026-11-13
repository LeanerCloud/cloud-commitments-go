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
		k := keyOf(a.Layer, a.Family)
		if !hasPurchase(purchases, k) {
			actions = append(actions, delayedHold(a, firstFire[k]))
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

// allocKey identifies one purchase slot: a layer, plus the instance family for
// a family-bound layer (empty family otherwise).
type allocKey struct {
	Layer  LayerType
	Family string
}

func keyOf(layer LayerType, family string) allocKey { return allocKey{Layer: layer, Family: family} }

func (k allocKey) String() string {
	if k.Family == "" {
		return "layer " + string(k.Layer)
	}
	return fmt.Sprintf("layer %s family %s", k.Layer, k.Family)
}

func allocationsByLayer(allocs []Allocation) (map[allocKey]Allocation, error) {
	byKey := make(map[allocKey]Allocation, len(allocs))
	for _, a := range allocs {
		k := keyOf(a.Layer, a.Family)
		if _, dup := byKey[k]; dup {
			return nil, fmt.Errorf("duplicate allocation for %s", k)
		}
		byKey[k] = a
	}
	return byKey, nil
}

// classifyTranches turns each immediate tranche into a purchase and records
// each slot's earliest later fire time. Orphan, overdue and bad-amount
// tranches, and a second immediate tranche on a slot, are errors.
func classifyTranches(now time.Time, tranches []Tranche, allocs map[allocKey]Allocation) ([]PlannedAction, map[allocKey]time.Time, error) {
	var purchases []PlannedAction
	firstFire := make(map[allocKey]time.Time)
	for i := range tranches {
		tr := &tranches[i]
		k := keyOf(tr.Layer, tr.Family)
		alloc, ok := allocs[k]
		if !ok {
			return nil, nil, fmt.Errorf("tranche %q: no allocation for %s", tr.ID, k)
		}
		switch {
		case tr.FireAfter.Before(now):
			return nil, nil, fmt.Errorf("tranche %q is overdue: fire_after %s is before now %s",
				tr.ID, tr.FireAfter.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
		case tr.FireAfter.After(now):
			if first, seen := firstFire[k]; !seen || tr.FireAfter.Before(first) {
				firstFire[k] = tr.FireAfter
			}
			continue
		}
		if hasPurchase(purchases, k) {
			return nil, nil, fmt.Errorf("%s has two immediate tranches", k)
		}
		amount, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
		if !ok || amount.Sign() <= 0 {
			return nil, nil, fmt.Errorf("tranche %q: amount %q is not a positive rational", tr.ID, tr.AmountUSDPerHour)
		}
		purchases = append(purchases, PlannedAction{
			Action:           ActionPurchase,
			Layer:            tr.Layer,
			Family:           tr.Family,
			AmountUSDPerHour: amount,
			Term:             tr.Term,
			PaymentOption:    tr.PaymentOption,
			Rationale:        alloc.Rationale,
			DataSources:      slices.Clone(alloc.DataSources),
		})
	}
	return purchases, firstFire, nil
}

func hasPurchase(purchases []PlannedAction, k allocKey) bool {
	return slices.ContainsFunc(purchases, func(p PlannedAction) bool { return keyOf(p.Layer, p.Family) == k })
}

func delayedHold(a Allocation, first time.Time) PlannedAction {
	when := "no tranche is scheduled for this layer"
	if !first.IsZero() {
		when = "first purchase is scheduled for " + first.UTC().Format(time.RFC3339)
	}
	return PlannedAction{
		Action:      ActionHold,
		Layer:       a.Layer,
		Family:      a.Family,
		Rationale:   fmt.Sprintf("%s; no immediate purchase: %s", a.Rationale, when),
		DataSources: slices.Clone(a.DataSources),
	}
}
