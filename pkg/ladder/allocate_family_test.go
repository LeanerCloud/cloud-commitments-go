package ladder

import (
	"math"
	"math/big"
	"strings"
	"testing"
)

func familyInput(byFamily map[string]float64) *AllocationInput {
	layers := awsLayers()
	return &AllocationInput{
		Config: validConfigAWS(),
		Layers: layers,
		Baseline: UsageBaseline{
			LowWaterUSDPerHour:       ptr(10.0),
			StableUSDPerHour:         ptr(4.0),
			StableUSDPerHourByFamily: byFamily,
		},
		LayerStates: zeroStates(layers),
		Now:         nowFixed(),
		InFlight:    noInFlight(), CommitmentRates: unitRates(),
	}
}

func baseRows(r *AllocateResult) []Allocation {
	var out []Allocation
	for _, a := range r.Allocations {
		if a.Layer == LayerEC2InstanceSP {
			out = append(out, a)
		}
	}
	return out
}

func TestAllocate_FamilySplitIsProportionalAndExact(t *testing.T) {
	t.Parallel()
	res, err := Allocate(familyInput(map[string]float64{"m5": 4.0 / 3, "c5": 4.0 / 3, "r5": 4.0 / 3}))
	if err != nil {
		t.Fatal(err)
	}
	rows := baseRows(res)
	if len(rows) != 3 {
		t.Fatalf("base rows = %d, want 3", len(rows))
	}
	sum := new(big.Rat)
	for i, want := range []string{"c5", "m5", "r5"} {
		if rows[i].Family != want {
			t.Fatalf("row %d family = %q, want %q (name order)", i, rows[i].Family, want)
		}
		sum.Add(sum, rows[i].GapUSDPerHour)
	}
	if got, _ := sum.Float64(); math.Abs(got-4) > 1e-12 {
		t.Fatalf("rows sum to %g, want the base gap 4 (to float rounding of the shares)", got)
	}
	exact := familyInput(map[string]float64{"m5": 1, "c5": 1, "r5": 2})
	res, err = Allocate(exact)
	if err != nil {
		t.Fatal(err)
	}
	sum = new(big.Rat)
	for _, r := range baseRows(res) {
		sum.Add(sum, r.GapUSDPerHour)
	}
	ratEq(t, "exact shares: rows sum to the base gap", sum, 4, 1)

	res, err = Allocate(familyInput(map[string]float64{"m5": 3, "c5": 1}))
	if err != nil {
		t.Fatal(err)
	}
	rows = baseRows(res)
	ratEq(t, "c5 share", rows[0].GapUSDPerHour, 1, 1)
	ratEq(t, "m5 share", rows[1].GapUSDPerHour, 3, 1)
	if !strings.Contains(rows[1].Rationale, "family=m5") {
		t.Fatalf("rationale does not name the family: %q", rows[1].Rationale)
	}
}

func TestAllocate_FamilySplitSkipsZeroShareAndSingleFamily(t *testing.T) {
	t.Parallel()
	res, err := Allocate(familyInput(map[string]float64{"m5": 4, "c5": 0}))
	if err != nil {
		t.Fatal(err)
	}
	rows := baseRows(res)
	if len(rows) != 1 || rows[0].Family != "m5" {
		t.Fatalf("rows = %+v, want only m5", rows)
	}
	ratEq(t, "m5 carries the whole base", rows[0].GapUSDPerHour, 4, 1)
}

func TestAllocate_NoFamilySplitKeepsFamilyAgnosticBase(t *testing.T) {
	t.Parallel()
	for _, m := range []map[string]float64{nil, {}} {
		res, err := Allocate(familyInput(m))
		if err != nil {
			t.Fatal(err)
		}
		rows := baseRows(res)
		if len(rows) != 1 || rows[0].Family != "" {
			t.Fatalf("rows = %+v, want one family-agnostic base row", rows)
		}
	}
}

func TestAllocate_FamilySplitIgnoredOnNonFamilyBase(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.Layers = []LayerSpec{
		{Type: LayerComputeSP, Roles: []LayerRole{RoleFlex}},
		{Type: LayerAzureReservation, Roles: []LayerRole{RoleBase, RoleBuffer}},
	}
	in.Config = validConfigAzure()
	in.LayerStates = zeroStates(in.Layers)
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Allocations {
		if a.Family != "" {
			t.Fatalf("allocation %+v carries a family on a non-family base", a)
		}
	}
}

func TestAllocate_RejectsInvalidFamilySplit(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]float64{
		"empty name": {"": 1},
		"negative":   {"m5": -1},
		"nan":        {"m5": math.NaN()},
		"inf":        {"m5": math.Inf(1)},
		"all zero":   {"m5": 0, "c5": 0},
		"fractions":  {"m5": 0.6, "c5": 0.4},
		"scaled up":  {"m5": 300, "c5": 100},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Allocate(familyInput(m)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestAllocate_FamilyRowsFlowThroughTranchesAndPlan(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig(singleStepRamp())
	tr, err := BuildTranches(baseInput(cfg, res.Allocations, planNow))
	if err != nil {
		t.Fatal(err)
	}
	p := mustBuildPlan(t, &PlanInput{
		Now: planNow, Scope: Scope{Provider: cfg.Scope.Provider, AccountID: "acct-test"},
		Baseline: in.Baseline, Result: res, Tranches: tr,
	})
	got := map[string]string{}
	for _, a := range purchasesOf(p) {
		if a.Layer == LayerEC2InstanceSP {
			got[a.Family] = a.AmountUSDPerHour.RatString()
		}
	}
	if len(got) != 2 || got["m5"] != "3" || got["c5"] != "1" {
		t.Fatalf("base purchases by family = %v, want m5=3 c5=1", got)
	}
}

// Existing base commitment is not known per family, so a family split would
// over-buy an already covered family; the base gap routes to flex instead.
func TestAllocate_FamilySplitWithheldWhenExistingBaseUnattributable(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.LayerStates = withExisting(in.LayerStates, LayerEC2InstanceSP, 1)
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	if rows := baseRows(res); len(rows) != 0 {
		t.Fatalf("base rows = %+v, want none while existing base is unattributable", rows)
	}
	var flex *Allocation
	for i, a := range res.Allocations {
		if a.Layer == LayerComputeSP {
			flex = &res.Allocations[i]
		}
	}
	if flex == nil || !strings.Contains(flex.Rationale, "not attributable per instance family") {
		t.Fatalf("flex allocation = %+v, want the routing note", flex)
	}
}

func TestAllocate_FamilySplitNeedsStable(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.Baseline.StableUSDPerHour = nil
	if _, err := Allocate(in); err == nil {
		t.Fatal("a family split without a stable estimate must fail loud")
	}
}

// The map is validated even when Allocate returns before splitting the gap.
func TestAllocate_InvalidFamilySplitFailsWhenTargetAlreadyMet(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": -1, "c5": 5})
	in.LayerStates = withExisting(in.LayerStates, LayerComputeSP, 50)
	if _, err := Allocate(in); err == nil {
		t.Fatal("want error for an invalid map on the target-met path")
	}
}

func TestAllocate_ZeroShareFamilyEmitsNoRowOrHold(t *testing.T) {
	t.Parallel()
	res, err := Allocate(familyInput(map[string]float64{"m5": 4, "c5": 0}))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Holds {
		if h.Family == "c5" {
			t.Fatalf("hold %+v for a zero-share family", h)
		}
	}
}
