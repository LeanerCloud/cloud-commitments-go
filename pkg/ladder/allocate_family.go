package ladder

import (
	"fmt"
	"math"
	"math/big"
	"slices"
)

// familySumTolerance is the relative slack allowed between the shares' sum and
// StableUSDPerHour, for float rounding in the provider.
const familySumTolerance = 1e-6

// familySplit is the validated per-family weighting of the stable base, with
// families in name order so the resulting allocations are deterministic.
type familySplit struct {
	weightTotal *big.Rat
	shares      map[string]*big.Rat
	// weights is each family's headroom: its share less its in-flight base
	// commitment (floored at 0). The base gap is divided by weight.
	weights map[string]*big.Rat
	names   []string
}

// parseFamilyShare validates one family name and share.
func parseFamilyShare(name string, v float64) (*big.Rat, error) {
	if name == "" {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family: empty family name")
	}
	if err := validateFamilyName(name); err != nil {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family: %w", err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family[%s]: %g must be finite and >= 0", name, v)
	}
	return new(big.Rat).SetFloat64(v), nil
}

// parseFamilySplit validates UsageBaseline.StableUSDPerHourByFamily. A nil or
// empty map means "not family split" and yields (nil, nil). A present map must
// have non-empty names, finite non-negative shares and a positive sum: a map
// that cannot carry a purchase is a provider bug, not a reason to buy
// family-less.
func parseFamilySplit(b UsageBaseline) (*familySplit, error) {
	byFamily := b.StableUSDPerHourByFamily
	if len(byFamily) == 0 {
		return nil, nil
	}
	if b.StableUSDPerHour == nil {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family is set but stable_usd_per_hour is nil")
	}
	fs := &familySplit{shares: make(map[string]*big.Rat, len(byFamily))}
	total := new(big.Rat)
	for name, v := range byFamily {
		r, err := parseFamilyShare(name, v)
		if err != nil {
			return nil, err
		}
		fs.shares[name] = r
		total.Add(total, r)
		fs.names = append(fs.names, name)
	}
	if total.Sign() <= 0 {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family: shares sum to zero")
	}
	// The shares are absolute USD/hr, so in-flight netting is per family; a
	// map on another scale would inflate or zero every family's headroom.
	got, _ := total.Float64()
	if want := *b.StableUSDPerHour; math.Abs(got-want) > familySumTolerance*math.Max(1, want) {
		return nil, fmt.Errorf("stable_usd_per_hour_by_family: shares sum to %g, want stable_usd_per_hour %g", got, want)
	}
	slices.Sort(fs.names)
	fs.weights, fs.weightTotal = fs.shares, total
	return fs, nil
}

// inFlightByFamily sums the OD-equivalent in-flight EC2 Instance SP tranches
// per family; tranches without a family are skipped. inFlightToOD has already
// rejected a family on a layer that is not family bound.
func inFlightByFamily(inFlight []InFlightTranche) (map[string]*big.Rat, error) {
	byFamily := make(map[string]*big.Rat)
	for i, t := range inFlight {
		if t.Family == "" {
			continue
		}
		name := fmt.Sprintf("in_flight[%d] (%s/%s)", i, t.Layer, t.Family)
		c, err := ratFromFloat(name+".commitment_usd_per_hour", t.CommitmentUSDPerHour)
		if err != nil {
			return nil, err
		}
		r, err := validateCommitmentRate(name, t.Rate)
		if err != nil {
			return nil, err
		}
		od := new(big.Rat).Quo(c, r)
		if cur, ok := byFamily[t.Family]; ok {
			cur.Add(cur, od)
		} else {
			byFamily[t.Family] = od
		}
	}
	return byFamily, nil
}

// netInFlight sets each family's weight to its share less the OD-equivalent
// in-flight base tranches already scheduled for that family (floored at 0), so
// a covered family is not topped up again while a ramp is running. Tranches
// without a family (rows from before the family dimension) are not attributable
// and only reduce the total gap, which computeBaseGap already nets.
func (fs *familySplit) netInFlight(inFlight []InFlightTranche) error {
	byFamily, err := inFlightByFamily(inFlight)
	if err != nil {
		return err
	}
	fs.weights = make(map[string]*big.Rat, len(fs.names))
	fs.weightTotal = new(big.Rat)
	for _, name := range fs.names {
		w := new(big.Rat).Set(fs.shares[name])
		if f, ok := byFamily[name]; ok {
			w.Sub(w, f)
		}
		if w.Sign() < 0 {
			w = new(big.Rat)
		}
		fs.weights[name] = w
		fs.weightTotal.Add(fs.weightTotal, w)
	}
	return nil
}

// validateTopology rejects a family split when the EC2 Instance SP layer carries
// a role other than base: a merged base+buffer or buffer-only layer would
// produce family-less purchases next to the family rows. A nil split accepts
// any topology.
func (fs *familySplit) validateTopology(layers []LayerSpec) error {
	if fs == nil {
		return nil
	}
	for _, ls := range layers {
		if ls.Type == LayerEC2InstanceSP && (len(ls.Roles) != 1 || ls.Roles[0] != RoleBase) {
			return fmt.Errorf("stable_usd_per_hour_by_family requires %s to carry only the base role, got %v", ls.Type, ls.Roles)
		}
	}
	return nil
}

// baseAllocations returns the base-role allocations: one per family with a
// positive share when the base layer is the family-bound EC2 Instance SP and a
// family split is present, otherwise the single family-agnostic allocation.
// Each family receives baseGap * weight / total weight in exact arithmetic, so
// the rows sum to baseGap and no row exceeds its family's headroom. The
// commitment rate is per layer, not per family.
func baseAllocations(ctx *allocationContext, baseLayer LayerType, baseGap *big.Rat) []Allocation {
	if ctx.family == nil || baseLayer != LayerEC2InstanceSP {
		return []Allocation{mkAllocation(ctx, baseLayer, baseGap, "base", ctx.baseNote)}
	}
	allocs := make([]Allocation, 0, len(ctx.family.names))
	for _, name := range ctx.family.names {
		weight := ctx.family.weights[name]
		if weight.Sign() == 0 {
			continue
		}
		gap := new(big.Rat).Mul(baseGap, new(big.Rat).Quo(weight, ctx.family.weightTotal))
		note := fmt.Sprintf("family=%s, stable_base=%s, headroom=%s", name, fmtRatUSD(ctx.family.shares[name]), fmtRatUSD(weight))
		a := mkAllocation(ctx, baseLayer, gap, "base", note)
		a.Family = name
		allocs = append(allocs, a)
	}
	return allocs
}

// withholdFamilyBase returns a routing note, non-empty when the family-split
// base purchase must be withheld this run, so the gap routes to flex. Existing
// base commitment on a total-basis baseline is known per layer only: splitting
// the remaining gap by share could push an already covered family past its own
// stable level. A residual baseline (AWS) excludes existing coverage per family
// already and nets no existing here.
func withholdFamilyBase(fs *familySplit, hasBase bool, baseLayer LayerType, nets map[LayerType]*big.Rat) string {
	if fs == nil || !hasBase || baseLayer != LayerEC2InstanceSP {
		return ""
	}
	if n := nets[baseLayer]; n != nil && n.Sign() > 0 {
		return "existing base commitment is not attributable per instance family; routing core gap to flex"
	}
	return ""
}

// applyFamilySplit parses the baseline's family split, nets in-flight base per
// family, and adjusts the base gap: withheld (routed to flex with a note) when
// existing base is unattributable, and capped at the families' headroom because
// the shares sum to stable only within float rounding. The split is nil when the
// baseline carries none.
func applyFamilySplit(in *AllocationInput, hasBase bool, baseLayer LayerType, nets map[LayerType]*big.Rat, baseGap *big.Rat, baseNote string) (*familySplit, *big.Rat, string, error) {
	fs, err := parseFamilySplit(in.Baseline)
	if err != nil || fs == nil {
		return nil, baseGap, baseNote, err
	}
	if err := fs.netInFlight(in.InFlight); err != nil {
		return nil, nil, "", err
	}
	if note := withholdFamilyBase(fs, hasBase, baseLayer, nets); note != "" && baseGap.Sign() > 0 {
		return fs, new(big.Rat), note, nil
	}
	if hasBase && baseLayer == LayerEC2InstanceSP && baseGap.Cmp(fs.weightTotal) > 0 {
		return fs, new(big.Rat).Set(fs.weightTotal), baseNote, nil
	}
	return fs, baseGap, baseNote, nil
}
