package ladder

import (
	"fmt"
	"math/big"
)

// BaselineBasis says what a UsageBaseline series measures (go#282).
//
// A total baseline (BaselineBasisTotal, or the empty legacy value) is all
// eligible on-demand-equivalent demand, covered or not; Allocate nets the
// existing commitment out of it: gap = min(target, low_water) - existing.
//
// A residual baseline (BaselineBasisResidual) is only the usage that existing
// commitments left uncovered, so existing coverage is already excluded.
// Subtracting existing commitment from it again under-buys and can claim
// "target already met" while usage is still uncovered. For a residual
// baseline Allocate sizes:
//
//	gap = low_water * target_pct/100 - in_flight
//
// which never exceeds the observed residual. With target_pct = 100 this is the
// exact remaining gap. Below 100 the true gap is pct*(C+R) - C, where C is the
// covered usage the residual excludes; it is at most pct*R, but it cannot be
// sized without C, so when any layer holds existing commitment the run is held
// rather than guessed (see residualPartialTargetHold). With no existing
// commitment in any layer the residual and total bases coincide.
//
// Expiring commitment is not added back for a residual baseline: its coverage
// is still in effect during the observed window, and converting native
// commitment dollars to the usage it covers needs rates the engine does not
// have. Usage that becomes uncovered after expiry shows up in later residual
// observations (a later top-up, never an over-buy).
//
// InFlight contract for a residual baseline: the series only reflects
// commitments whose coverage was already observed. Callers must include in
// InFlight every commitment that is active but not yet reflected in the
// series (for example one bought after the last observed day, given
// cost-data reporting lag), or Allocate re-plans it.
type BaselineBasis string

const (
	// BaselineBasisTotal marks a series of total eligible on-demand-equivalent
	// demand (covered plus uncovered). The empty value means the same.
	BaselineBasisTotal BaselineBasis = "total"
	// BaselineBasisResidual marks a series of usage left uncovered by existing
	// commitments (already net of them).
	BaselineBasisResidual BaselineBasis = "residual"
)

// Validate rejects an unrecognized basis: guessing one would either
// double-subtract existing commitment or skip netting it.
func (b BaselineBasis) Validate() error {
	switch b {
	case "", BaselineBasisTotal, BaselineBasisResidual:
		return nil
	default:
		return fmt.Errorf("baseline basis %q is not recognized (want %q or %q)", b, BaselineBasisTotal, BaselineBasisResidual)
	}
}

func (b BaselineBasis) isResidual() bool { return b == BaselineBasisResidual }

// existingForBasis returns the existing commitment to subtract from the
// baseline and the per-layer nets the base split counts against the stable
// level. A residual baseline already excludes existing coverage, so both are
// zero for it.
func existingForBasis(basis BaselineBasis, nets map[LayerType]*big.Rat, existingTotal *big.Rat) (gapExisting *big.Rat, gapNets map[LayerType]*big.Rat) {
	if !basis.isResidual() {
		return existingTotal, nets
	}
	return new(big.Rat), map[LayerType]*big.Rat{}
}

// residualPartialTargetHold returns a Hold when a residual baseline is paired
// with a target below 100% while some layer holds existing commitment: the
// partial target depends on the covered usage the residual excludes, so the
// gap cannot be sized from the baseline. gap is the pct*R - in_flight upper
// bound already known to exceed the minimum (a smaller bound is reported as
// "target already met" before this is called). Returns nil when the residual
// gap is exact.
func residualPartialTargetHold(in *AllocationInput, lowWater, gap *big.Rat) *AllocateResult {
	if !in.Baseline.Basis.isResidual() || in.Config.TargetCoveragePct >= 100 || !hasExistingCommitment(in.Layers, in.LayerStates) {
		return nil
	}
	flexLayer, _ := findLayerForRole(in.Layers, RoleFlex)
	rationale := fmt.Sprintf(
		"residual baseline cannot size a partial target: target=%.2f%% < 100%% with existing commitment; the gap depends on covered usage the residual excludes (upper bound %s from low_water=%s), so nothing is bought; use target 100%% or a total-demand baseline",
		in.Config.TargetCoveragePct, fmtRatUSD(gap), fmtRatUSD(lowWater),
	)
	return &AllocateResult{Holds: []PlannedAction{{
		Action:      ActionHold,
		Layer:       flexLayer,
		Rationale:   rationale,
		DataSources: in.DataSources,
	}}}
}

// hasExistingCommitment reports whether any layer holds existing commitment
// (expiring or not: until it expires its coverage is excluded from a residual
// baseline). States are validated non-nil before this runs.
func hasExistingCommitment(layers []LayerSpec, states map[LayerType]LayerState) bool {
	for _, ls := range layers {
		if *states[ls.Type].ExistingUSDPerHour > 0 {
			return true
		}
	}
	return false
}

// basisNote is appended to rationales so a residual-basis decision is
// explainable: "existing" is shown but was not subtracted.
func basisNote(basis BaselineBasis) string {
	if !basis.isResidual() {
		return ""
	}
	return "; baseline_basis=residual (existing commitment already excluded from low_water, not subtracted again)"
}
