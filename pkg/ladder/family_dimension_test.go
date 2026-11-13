package ladder

import (
	"math/big"
	"strings"
	"testing"
)

func familyAlloc(family string, gapUSD int64) Allocation {
	a := mkAlloc(LayerEC2InstanceSP, gapUSD)
	a.Family = family
	return a
}

func TestFamilyOnNonFamilyLayerIsRejected(t *testing.T) {
	t.Parallel()
	tr := Tranche{
		ID: "t1", RunID: "r1", Status: TrancheStatusScheduled, FireAfter: planNow,
		AmountUSDPerHour: "1", Layer: LayerComputeSP, Family: "m5",
		Term: Term1Year, PaymentOption: PaymentNoUpfront,
	}
	if err := tr.Validate(); err == nil || !strings.Contains(err.Error(), "only valid on layer") {
		t.Fatalf("tranche err = %v, want family-on-wrong-layer error", err)
	}
	tr.Layer = LayerEC2InstanceSP
	if err := tr.Validate(); err != nil {
		t.Fatalf("EC2 Instance SP tranche with family: %v", err)
	}
	for _, bad := range []string{"m5\n", "m5\x00"} {
		tr.Family = bad
		if err := tr.Validate(); err == nil || !strings.Contains(err.Error(), "whitespace or control") {
			t.Fatalf("family %q: err = %v, want a family-name error", bad, err)
		}
	}

	pa := PlannedAction{Action: ActionHold, Layer: LayerConvertibleRI, Family: "m5", Rationale: "x"}
	if err := pa.Validate(); err == nil || !strings.Contains(err.Error(), "only valid on layer") {
		t.Fatalf("action err = %v, want family-on-wrong-layer error", err)
	}
}

func TestApplyCapGuardrail_KeepsFamily(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{familyAlloc("m5", 6), familyAlloc("c5", 4)}
	for i := range allocs {
		allocs[i].OnDemandEquivalentUSDPerHour = new(big.Rat).Set(allocs[i].GapUSDPerHour)
	}
	got, err := applyCapGuardrail(allocs, ptr(5.0))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Family != "m5" || got[1].Family != "c5" {
		t.Fatalf("families after cap = %q, %q; want m5, c5", got[0].Family, got[1].Family)
	}
}

func TestTruncatePurchases_FamilyTieBreak(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{familyAlloc("m7", 3), familyAlloc("c5", 3), familyAlloc("m5", 3)}
	kept, holds := truncatePurchases(allocs, 2, 2, nil)
	if len(kept) != 2 || kept[0].Family != "c5" || kept[1].Family != "m5" || len(holds) != 1 {
		t.Fatalf("kept = %+v holds = %d; want c5, m5 kept and m7 held", kept, len(holds))
	}
}

func TestBuildPlan_FamilyRowsOnOneLayer(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{familyAlloc("m5", 3), familyAlloc("c5", 2)}
	cfg := baseConfig(singleStepRamp())
	tr, err := BuildTranches(baseInput(cfg, allocs, planNow))
	if err != nil {
		t.Fatalf("BuildTranches: %v", err)
	}
	for _, x := range tr.Tranches {
		if x.Family == "" {
			t.Fatalf("tranche %s has no family", x.ID)
		}
	}
	in := &PlanInput{
		Now: planNow, Scope: Scope{Provider: cfg.Scope.Provider, AccountID: "acct-test"},
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
		Result:   &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1), Allocations: allocs},
		Tranches: tr,
	}
	p := mustBuildPlan(t, in)
	got := map[string]string{}
	for _, a := range purchasesOf(p) {
		got[a.Family] = a.AmountUSDPerHour.RatString()
	}
	if len(got) != 2 || got["m5"] != "3" || got["c5"] != "2" {
		t.Fatalf("purchases by family = %v, want m5=3 c5=2", got)
	}

	dup := &PlanInput{
		Now: in.Now, Scope: in.Scope, Baseline: in.Baseline, Tranches: tr,
		Result: &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1),
			Allocations: []Allocation{familyAlloc("m5", 3), familyAlloc("m5", 2)}},
	}
	if _, err := BuildPlan(dup); err == nil || !strings.Contains(err.Error(), "duplicate allocation") {
		t.Fatalf("same-family duplicate err = %v, want duplicate allocation", err)
	}
}

func TestBuildPlan_DelayedFamilyHoldKeepsFamily(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{familyAlloc("m5", 3)}
	in := &PlanInput{
		Now: planNow, Scope: Scope{Provider: baseConfig(singleStepRamp()).Scope.Provider, AccountID: "acct-test"},
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
		Result:   &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1), Allocations: allocs},
		Tranches: &TrancheResult{},
	}
	p := mustBuildPlan(t, in)
	if len(p.Actions) != 1 || p.Actions[0].Action != ActionHold || p.Actions[0].Family != "m5" {
		t.Fatalf("actions = %+v, want one m5 hold", p.Actions)
	}
}

func TestDroppedFamilyAllocationsKeepFamilyOnTheirHolds(t *testing.T) {
	t.Parallel()
	tiny := familyAlloc("m5", 0)
	tiny.GapUSDPerHour = big.NewRat(1, 1000000)
	_, holds := dropSubMinimumAllocations([]Allocation{tiny}, nil)
	if len(holds) != 1 || holds[0].Family != "m5" {
		t.Fatalf("sub-minimum holds = %+v, want one m5 hold", holds)
	}

	norate := familyAlloc("c5", 3)
	norate.OnDemandEquivalentUSDPerHour = big.NewRat(3, 1)
	_, holds = convertAllocations([]Allocation{norate}, map[LayerType]*big.Rat{}, nil)
	if len(holds) != 1 || holds[0].Family != "c5" {
		t.Fatalf("no-rate holds = %+v, want one c5 hold", holds)
	}

	_, holds = truncatePurchases([]Allocation{familyAlloc("m5", 3)}, 0, 1, nil)
	if len(holds) != 1 || holds[0].Family != "m5" {
		t.Fatalf("truncation holds = %+v, want one m5 hold", holds)
	}
}

func TestExplain_NamesTheFamilyOfEachRow(t *testing.T) {
	t.Parallel()
	allocs := []Allocation{familyAlloc("m5", 3), familyAlloc("c5", 2)}
	cfg := baseConfig(singleStepRamp())
	tr, err := BuildTranches(baseInput(cfg, allocs, planNow))
	if err != nil {
		t.Fatal(err)
	}
	p := mustBuildPlan(t, &PlanInput{
		Now: planNow, Scope: Scope{Provider: cfg.Scope.Provider, AccountID: "acct-test"},
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
		Result:   &AllocateResult{NetExistingUSDPerHour: big.NewRat(60, 1), Allocations: allocs},
		Tranches: tr,
	})
	out := p.Explain()
	for _, want := range []string{"ec2-instance-sp/m5", "ec2-instance-sp/c5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Explain missing %q:\n%s", want, out)
		}
	}
}
