package ladder

import (
	"math/big"
	"strings"
	"testing"
)

func capsConfig(hourly *float64, maxActions int) *LadderConfig {
	cfg := baseConfig(singleStepRamp())
	cfg.MaxHourlyCommitPerRun = hourly
	cfg.MaxActionsPerRun = maxActions
	return cfg
}

func capsPlan(actions ...PlannedAction) *LadderPlan {
	return &LadderPlan{
		Scope:       baseConfig(singleStepRamp()).Scope,
		GeneratedAt: planNow,
		Actions:     actions,
	}
}

func capsPurchase(layer LayerType, usd int64) PlannedAction {
	return PlannedAction{
		Action:           ActionPurchase,
		Layer:            layer,
		AmountUSDPerHour: new(big.Rat).SetInt64(usd),
		Term:             Term1Year,
		PaymentOption:    PaymentNoUpfront,
		Rationale:        "test purchase",
	}
}

func capsReshape() PlannedAction {
	return PlannedAction{Action: ActionReshape, Layer: LayerConvertibleRI, Rationale: "test reshape"}
}

func capsHold() PlannedAction {
	return PlannedAction{Action: ActionHold, Layer: LayerComputeSP, Rationale: "test hold"}
}

func TestValidateRunCaps_PurchaseSumOverHourlyCapRejected(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 2), capsPurchase(LayerEC2InstanceSP, 2))
	err := p.ValidateRunCaps(capsConfig(ptr(3.0), 10))
	if err == nil || !strings.Contains(err.Error(), "max_hourly_commit_per_run") {
		t.Fatalf("err = %v, want hourly cap rejection", err)
	}
}

func TestValidateRunCaps_PurchaseSumAtHourlyCapAccepted(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 2), capsPurchase(LayerEC2InstanceSP, 3))
	if err := p.ValidateRunCaps(capsConfig(ptr(5.0), 10)); err != nil {
		t.Fatalf("sum equal to cap must pass: %v", err)
	}
}

func TestValidateRunCaps_NilHourlyCapIsNoLimit(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 1000000))
	if err := p.ValidateRunCaps(capsConfig(nil, 10)); err != nil {
		t.Fatalf("nil hourly cap must mean no limit: %v", err)
	}
}

func TestValidateRunCaps_PurchasesAndReshapesOverMaxActionsRejected(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 1), capsReshape())
	err := p.ValidateRunCaps(capsConfig(nil, 1))
	if err == nil || !strings.Contains(err.Error(), "max_actions_per_run") {
		t.Fatalf("err = %v, want action cap rejection", err)
	}
}

func TestValidateRunCaps_PurchasesAndReshapesAtMaxActionsAccepted(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 1), capsReshape())
	if err := p.ValidateRunCaps(capsConfig(nil, 2)); err != nil {
		t.Fatalf("count equal to cap must pass: %v", err)
	}
}

func TestValidateRunCaps_HoldsAreNotCounted(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 1), capsHold(), capsHold(), capsHold())
	if err := p.ValidateRunCaps(capsConfig(nil, 1)); err != nil {
		t.Fatalf("holds must not count toward max_actions_per_run: %v", err)
	}
}

func TestValidateRunCaps_RejectsNilPlanAndConfig(t *testing.T) {
	t.Parallel()
	var nilPlan *LadderPlan
	if err := nilPlan.ValidateRunCaps(capsConfig(nil, 1)); err == nil {
		t.Error("nil plan must be rejected")
	}
	if err := capsPlan().ValidateRunCaps(nil); err == nil {
		t.Error("nil config must be rejected")
	}
}

func TestValidateRunCaps_RejectsInvalidLimits(t *testing.T) {
	t.Parallel()
	if err := capsPlan().ValidateRunCaps(capsConfig(nil, 0)); err == nil {
		t.Error("max_actions_per_run 0 must be rejected, not treated as a pass")
	}
	if err := capsPlan().ValidateRunCaps(capsConfig(ptr(-1.0), 1)); err == nil {
		t.Error("negative hourly cap must be rejected")
	}
}

func TestValidateRunCaps_RejectsMalformedPurchase(t *testing.T) {
	t.Parallel()
	bad := capsPurchase(LayerComputeSP, 1)
	bad.AmountUSDPerHour = nil
	if err := capsPlan(bad).ValidateRunCaps(capsConfig(ptr(5.0), 10)); err == nil {
		t.Error("purchase with nil amount must be rejected by plan validation")
	}
}

// The real pipeline: Allocate caps the gap at $5/hr, BuildTranches and
// BuildPlan carry it through, and the immediate purchases sum to exactly the cap.
func TestValidateRunCaps_AllocatePipelineOutputAtCapPasses(t *testing.T) {
	t.Parallel()
	cfg := validConfigAWS()
	cfg.Ramp = singleStepRamp()
	cfg.MaxHourlyCommitPerRun = ptr(5.0)
	layers := awsLayers()
	result, err := Allocate(&AllocationInput{
		Config:      cfg,
		Layers:      layers,
		Baseline:    UsageBaseline{LowWaterUSDPerHour: ptr(10.0), StableUSDPerHour: ptr(4.0)},
		LayerStates: zeroStates(layers),
		Now:         nowFixed(),
		InFlight:    noInFlight(), CommitmentRates: unitRates(),
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	tr, err := BuildTranches(baseInput(&cfg, result.Allocations, nowFixed()))
	if err != nil {
		t.Fatalf("BuildTranches: %v", err)
	}
	plan, err := BuildPlan(&PlanInput{
		Target:   big.NewRat(100, 1),
		Result:   result,
		Tranches: tr,
		Scope:    cfg.Scope,
		Now:      nowFixed(),
		Baseline: UsageBaseline{LookbackDays: 30, Percentile: 5},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := plan.ValidateRunCaps(&cfg); err != nil {
		t.Fatalf("pipeline output must pass its own caps: %v", err)
	}
	total := new(big.Rat)
	for _, a := range purchasesOf(plan) {
		total.Add(total, a.AmountUSDPerHour)
	}
	if total.Cmp(big.NewRat(5, 1)) != 0 {
		t.Fatalf("immediate purchases = %s, want exactly the 5 cap", total.RatString())
	}
	if !strings.Contains(plan.Actions[0].Rationale, "capped by max_hourly_commit_per_run") {
		t.Fatalf("rationale %q does not show the cap scaling, so the edge is not exercised", plan.Actions[0].Rationale)
	}
}

// A reshape carries no amount, so it must never be summed toward the hourly cap.
func TestValidateRunCaps_ReshapeIsNotSummedTowardHourlyCap(t *testing.T) {
	t.Parallel()
	p := capsPlan(capsPurchase(LayerComputeSP, 2), capsReshape())
	if err := p.ValidateRunCaps(capsConfig(ptr(2.0), 10)); err != nil {
		t.Fatalf("reshape must not add to the hourly total: %v", err)
	}
	if err := p.ValidateRunCaps(capsConfig(ptr(1.5), 10)); err == nil {
		t.Fatal("purchase of 2 must still exceed a 1.5 cap alongside a reshape")
	}
}
