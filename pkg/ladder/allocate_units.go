package ladder

import (
	"fmt"
	"math"
	"math/big"
)

// Unit rule (go#349): sizing is on-demand-equivalent (OD) USD/hr, everything
// that is bought or stored is commitment USD/hr. A commitment rate r in (0, 1]
// is the commitment dollars needed per OD dollar covered, so
//
//	commitment = od * r        od = commitment / r
//
// Allocate converts exactly once, after the gap is split across layers and
// before the per-run cap, the minimum-allocatable filter and BuildTranches.
// Conversion is exact big.Rat arithmetic; rounding to the provider's
// commitment precision happens at the purchase boundary and MUST round down so
// a purchase never exceeds the planned commitment.

// InFlightTranche is a scheduled (not-yet-fired) ladder tranche in commitment
// USD/hr together with the rate that was stored with it when it was planned.
// Allocate converts it back to OD with that rate (od = commitment / Rate), so
// a tranche is netted at the rate it was sized with, not at today's.
type InFlightTranche struct {
	Layer                LayerType
	CommitmentUSDPerHour float64
	// Rate is the commitment rate stored with the tranche. Required in (0, 1];
	// a tranche without a valid rate is a data error, never treated as 1:1.
	Rate float64
}

// validateCommitmentRate converts a float rate to an exact rational and rejects
// anything outside (0, 1] (NaN, Inf, non-positive, or above 1: a commitment
// cheaper than nothing or dearer than on-demand is a data error).
func validateCommitmentRate(name string, v float64) (*big.Rat, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("%s: commitment rate %g is not finite", name, v)
	}
	if v <= 0 || v > 1 {
		return nil, fmt.Errorf("%s: commitment rate %g must be in (0, 1]", name, v)
	}
	return new(big.Rat).SetFloat64(v), nil
}

// parseCommitmentRates validates every supplied rate. Absent layers are not an
// error here: a layer without a rate only matters if it receives an
// allocation (convertAllocations holds it).
func parseCommitmentRates(rates map[LayerType]float64) (map[LayerType]*big.Rat, error) {
	out := make(map[LayerType]*big.Rat, len(rates))
	for layer, v := range rates {
		r, err := validateCommitmentRate("commitment_rates["+string(layer)+"]", v)
		if err != nil {
			return nil, err
		}
		out[layer] = r
	}
	return out, nil
}

// inFlightToOD converts the in-flight tranches to OD-equivalent USD/hr, per
// layer and in total. Every tranche must name a configured layer and carry a
// valid stored rate.
func inFlightToOD(layers []LayerSpec, inFlight []InFlightTranche) (map[LayerType]*big.Rat, *big.Rat, error) {
	known := make(map[LayerType]bool, len(layers))
	for _, ls := range layers {
		known[ls.Type] = true
	}
	perLayer := make(map[LayerType]*big.Rat, len(inFlight))
	total := new(big.Rat)
	for i, t := range inFlight {
		name := fmt.Sprintf("in_flight[%d] (%s)", i, t.Layer)
		if !known[t.Layer] {
			return nil, nil, fmt.Errorf("%s: layer is not in the supported layers; its commitment would be silently un-netted", name)
		}
		c, err := ratFromFloat(name+".commitment_usd_per_hour", t.CommitmentUSDPerHour)
		if err != nil {
			return nil, nil, err
		}
		r, err := validateCommitmentRate(name, t.Rate)
		if err != nil {
			return nil, nil, err
		}
		od := new(big.Rat).Quo(c, r)
		if cur, ok := perLayer[t.Layer]; ok {
			cur.Add(cur, od)
		} else {
			perLayer[t.Layer] = od
		}
		total.Add(total, od)
	}
	return perLayer, total, nil
}

// convertAllocations turns OD-sized allocations into commitment-sized ones.
// An allocation on a layer with no rate is dropped with an explainable Hold:
// its share is not re-routed to another layer and no rate is ever guessed.
func convertAllocations(allocs []Allocation, rates map[LayerType]*big.Rat, dataSources []string) ([]Allocation, []PlannedAction) {
	out := make([]Allocation, 0, len(allocs))
	var holds []PlannedAction
	for _, a := range allocs {
		r, ok := rates[a.Layer]
		if !ok {
			holds = append(holds, PlannedAction{
				Action: ActionHold,
				Layer:  a.Layer,
				Rationale: fmt.Sprintf("no commitment rate for layer %s: skipped %s on-demand-equivalent (never assumed 1:1)",
					a.Layer, fmtRatUSD(a.OnDemandEquivalentUSDPerHour)),
				DataSources: dataSources,
			})
			continue
		}
		a.Rate = new(big.Rat).Set(r)
		a.GapUSDPerHour = new(big.Rat).Mul(a.OnDemandEquivalentUSDPerHour, r)
		a.Rationale += fmt.Sprintf("; commitment_rate=%s, commitment=%s", fmtRatRate(r), fmtRatUSD(a.GapUSDPerHour))
		out = append(out, a)
	}
	return out, holds
}

// fmtRatRate renders a commitment rate with 4 decimals for rationales.
func fmtRatRate(r *big.Rat) string {
	f, _ := r.Float64()
	return fmt.Sprintf("%.4f", f)
}
