package ladder

import (
	"math/big"
	"strings"
	"testing"
	"time"
)

// planFixture: one $5/hr compute-sp allocation, net existing $60, target $100.
func planFixture(t *testing.T, ramp RampSchedule, now time.Time) *PlanInput {
	t.Helper()
	allocs := []Allocation{mkAlloc(LayerComputeSP, 5)}
	tr, err := BuildTranches(baseInput(baseConfig(ramp), allocs, now))
	if err != nil {
		t.Fatalf("BuildTranches: %v", err)
	}
	return &PlanInput{
		Now:      now,
		Scope:    Scope{Provider: baseConfig(ramp).Scope.Provider, AccountID: "acct-test"},
		Target:   big.NewRat(100, 1),
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
		Result:   &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1), Allocations: allocs},
		Tranches: tr,
	}
}

func purchasesOf(p *LadderPlan) []PlannedAction {
	var out []PlannedAction
	for _, a := range p.Actions {
		if a.Action == ActionPurchase {
			out = append(out, a)
		}
	}
	return out
}

func mustBuildPlan(t *testing.T, in *PlanInput) *LadderPlan {
	t.Helper()
	p, err := BuildPlan(in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return p
}

var planNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// halfRamp has float-exact fractions so step amounts are exact rationals.
func halfRamp() RampSchedule {
	return RampSchedule{Steps: []RampStep{{AfterDays: 0, Fraction: 0.5}, {AfterDays: 30, Fraction: 0.5}}}
}

// The purchase is the immediate step ($2.50 = 0.5 * $5), never the $5 allocation
// total, and it carries the allocation's rationale and data sources.
func TestBuildPlan_ImmediateTrancheBecomesPurchase(t *testing.T) {
	t.Parallel()
	in := planFixture(t, halfRamp(), planNow)
	p := mustBuildPlan(t, in)

	got := purchasesOf(p)
	if len(got) != 1 {
		t.Fatalf("purchases = %d, want 1", len(got))
	}
	a := got[0]
	if a.AmountUSDPerHour.Cmp(big.NewRat(5, 2)) != 0 {
		t.Errorf("amount = %s, want 5/2 (the immediate step, not the $5 total)", a.AmountUSDPerHour.RatString())
	}
	if a.Layer != LayerComputeSP || a.Term != Term1Year || a.PaymentOption != PaymentNoUpfront {
		t.Errorf("layer/term/payment = %s/%s/%s", a.Layer, a.Term, a.PaymentOption)
	}
	if a.Rationale != in.Result.Allocations[0].Rationale {
		t.Errorf("rationale = %q", a.Rationale)
	}
	a.DataSources[0] = "mutated"
	if in.Result.Allocations[0].DataSources[0] != "test-source" {
		t.Error("purchase DataSources aliases the allocation's slice")
	}
}

func TestBuildPlan_FutureTranchesAreNotActions(t *testing.T) {
	t.Parallel()
	p := mustBuildPlan(t, planFixture(t, halfRamp(), planNow))
	if len(p.Actions) != 1 {
		t.Fatalf("actions = %d, want only the immediate purchase", len(p.Actions))
	}
}

// AddDate drops time.Now()'s monotonic reading; == would miss the tranche.
func TestBuildPlan_ImmediateUsesTimeEqual(t *testing.T) {
	t.Parallel()
	now := time.Now()
	in := planFixture(t, halfRamp(), now)
	for _, tr := range in.Tranches.Tranches {
		if tr.StepIndex == 0 && tr.FireAfter == now { //nolint:staticcheck // QF1009: == is the point of this guard
			t.Fatal("fixture is not meaningful: FireAfter == Now under ==")
		}
	}
	if n := len(purchasesOf(mustBuildPlan(t, in))); n != 1 {
		t.Fatalf("purchases = %d, want 1", n)
	}
}

func TestBuildPlan_ActionOrder(t *testing.T) {
	t.Parallel()
	in := planFixture(t, halfRamp(), planNow)
	in.Result.Reshapes = []PlannedAction{{Action: ActionReshape, Layer: LayerConvertibleRI, Rationale: "reshape r"}}
	in.Result.Holds = []PlannedAction{{Action: ActionHold, Layer: LayerEC2InstanceSP, Rationale: "hold h"}}
	p := mustBuildPlan(t, in)

	want := []ActionType{ActionPurchase, ActionReshape, ActionHold}
	if len(p.Actions) != len(want) {
		t.Fatalf("actions = %d, want %d", len(p.Actions), len(want))
	}
	for i, w := range want {
		if p.Actions[i].Action != w {
			t.Errorf("Actions[%d] = %s, want %s", i, p.Actions[i].Action, w)
		}
	}
}

func TestBuildPlan_Gap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		target *big.Rat
		want   *big.Rat
	}{
		{"target minus existing", big.NewRat(100, 1), big.NewRat(40, 1)},
		{"over target is signed negative", big.NewRat(50, 1), big.NewRat(-10, 1)},
		{"nil target leaves gap nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := planFixture(t, halfRamp(), planNow)
			in.Target = c.target
			p := mustBuildPlan(t, in)
			if c.want == nil {
				if p.GapUSDPerHour != nil {
					t.Fatalf("gap = %s, want nil", p.GapUSDPerHour.RatString())
				}
				return
			}
			if p.GapUSDPerHour == nil || p.GapUSDPerHour.Cmp(c.want) != 0 {
				t.Fatalf("gap = %v, want %s", p.GapUSDPerHour, c.want.RatString())
			}
			if p.ExistingUSDPerHour.Cmp(big.NewRat(60, 1)) != 0 {
				t.Errorf("existing = %s, want 60", p.ExistingUSDPerHour.RatString())
			}
		})
	}
}

// Target and Existing are copies: mutating the plan must not reach the inputs.
func TestBuildPlan_CopiesRatInputs(t *testing.T) {
	t.Parallel()
	in := planFixture(t, halfRamp(), planNow)
	p := mustBuildPlan(t, in)
	p.TargetUSDPerHour.SetInt64(1)
	p.ExistingUSDPerHour.SetInt64(1)
	if in.Target.Cmp(big.NewRat(100, 1)) != 0 || in.Result.NetExistingUSDPerHour.Cmp(big.NewRat(60, 1)) != 0 {
		t.Errorf("plan mutation reached inputs: target=%s existing=%s",
			in.Target.RatString(), in.Result.NetExistingUSDPerHour.RatString())
	}
}

// Two layers: A has an immediate tranche, B is fully delayed. Only B gets a
// hold, and it carries B's own first-fire time.
func TestBuildPlan_PerLayerHoldsForMixedRamps(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{mkAlloc(LayerComputeSP, 5), mkAlloc(LayerConvertibleRI, 4)}
	now := planNow
	cfg := baseConfig(halfRamp())
	tr, err := BuildTranches(baseInput(cfg, allocs, now))
	if err != nil {
		t.Fatalf("BuildTranches: %v", err)
	}
	// Keep only layer A's immediate tranche, and give layer B a single
	// delayed tranche at +30 days.
	var kept []Tranche
	for _, x := range tr.Tranches {
		if x.Layer == LayerComputeSP || x.FireAfter.After(now) {
			kept = append(kept, x)
		}
	}
	var bLater time.Time
	for i := range kept {
		if kept[i].Layer == LayerConvertibleRI {
			kept[i].FireAfter = now.AddDate(0, 0, 30)
			bLater = kept[i].FireAfter
		}
	}
	p := mustBuildPlan(t, &PlanInput{
		Now: now, Scope: Scope{Provider: cfg.Scope.Provider, AccountID: "acct-test"},
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
		Result:   &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1), Allocations: allocs},
		Tranches: &TrancheResult{Tranches: kept},
	})
	if got := purchasesOf(p); len(got) != 1 || got[0].Layer != LayerComputeSP {
		t.Fatalf("purchases = %+v, want one compute-sp purchase", got)
	}
	var holds []PlannedAction
	for _, a := range p.Actions {
		if a.Action == ActionHold {
			holds = append(holds, a)
		}
	}
	if len(holds) != 1 || holds[0].Layer != LayerConvertibleRI ||
		!strings.Contains(holds[0].Rationale, bLater.UTC().Format(time.RFC3339)) {
		t.Fatalf("holds = %+v, want one convertible-ri hold at %s", holds, bLater)
	}
}

// Plan.Validate is the final guard: a bad rationale on an allocation that
// reaches a purchase must fail the build.
func TestBuildPlan_InvalidBuiltPlanIsRejected(t *testing.T) {
	t.Parallel()
	in := planFixture(t, halfRamp(), planNow)
	in.Result.Allocations[0].Rationale = ""
	_, err := BuildPlan(in)
	if err == nil || !strings.Contains(err.Error(), "built plan is invalid") {
		t.Fatalf("err = %v, want 'built plan is invalid'", err)
	}
}

// A fully delayed ramp has no purchase; the plan says when the first one fires.
func TestBuildPlan_LayerWithoutImmediateTrancheGetsHold(t *testing.T) {
	t.Parallel()
	p := mustBuildPlan(t, planFixture(t, delayedRamp(), planNow))
	if len(purchasesOf(p)) != 0 || len(p.Actions) != 1 {
		t.Fatalf("actions = %+v, want one hold and no purchase", p.Actions)
	}
	h := p.Actions[0]
	wantWhen := "first purchase is scheduled for " + planNow.AddDate(0, 0, 30).Format(time.RFC3339)
	if h.Action != ActionHold || h.Layer != LayerComputeSP || !strings.Contains(h.Rationale, wantWhen) {
		t.Errorf("hold = %+v, want a compute-sp hold containing %q", h, wantWhen)
	}
}

func TestBuildPlan_EmptyTranchesYieldHoldsOnly(t *testing.T) {
	t.Parallel()
	in := planFixture(t, halfRamp(), planNow)
	in.Tranches = &TrancheResult{}
	p := mustBuildPlan(t, in)
	if len(p.Actions) != 1 || p.Actions[0].Action != ActionHold ||
		!strings.Contains(p.Actions[0].Rationale, "no tranche is scheduled for this layer") {
		t.Fatalf("actions = %+v, want one 'nothing scheduled' hold", p.Actions)
	}
}

func TestBuildPlan_ExplainRendersPurchase(t *testing.T) {
	t.Parallel()
	out := mustBuildPlan(t, planFixture(t, halfRamp(), planNow)).Explain()
	for _, want := range []string{"purchase compute-sp $2.50/hr", "Gap:      $40.00/hr", "Data sources: test-source"} {
		if !strings.Contains(out, want) {
			t.Errorf("Explain() missing %q:\n%s", want, out)
		}
	}
}

func TestBuildPlan_Rejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(in *PlanInput)
		wantErr string
	}{
		{"zero now", func(in *PlanInput) { in.Now = time.Time{} }, "now must not be zero"},
		{"nil result", func(in *PlanInput) { in.Result = nil }, "allocate result must not be nil"},
		{"nil net existing", func(in *PlanInput) { in.Result.NetExistingUSDPerHour = nil }, "no net existing"},
		{"nil tranches", func(in *PlanInput) { in.Tranches = nil }, "tranche result must not be nil"},
		{"tranche without allocation", func(in *PlanInput) { in.Result.Allocations = nil }, "no allocation for layer"},
		{"duplicate allocation layer", func(in *PlanInput) {
			in.Result.Allocations = append(in.Result.Allocations, in.Result.Allocations[0])
		}, "duplicate allocation"},
		{"two immediate tranches", func(in *PlanInput) {
			dup := in.Tranches.Tranches[0]
			dup.ID = "dup"
			in.Tranches.Tranches = append(in.Tranches.Tranches, dup)
		}, "two immediate tranches"},
		{"overdue tranche", func(in *PlanInput) {
			in.Tranches.Tranches[1].FireAfter = in.Now.Add(-time.Hour)
		}, "overdue"},
		{"non-positive amount", func(in *PlanInput) { in.Tranches.Tranches[0].AmountUSDPerHour = "0" }, "not a positive rational"},
		{"unparseable amount", func(in *PlanInput) { in.Tranches.Tranches[0].AmountUSDPerHour = "abc" }, "not a positive rational"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := planFixture(t, halfRamp(), planNow)
			c.mutate(in)
			_, err := BuildPlan(in)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, c.wantErr)
			}
		})
	}
	if _, err := BuildPlan(nil); err == nil || !strings.Contains(err.Error(), "must not be nil") {
		t.Errorf("BuildPlan(nil) err = %v", err)
	}
}
