package ladder

import (
	"math/big"
	"strings"
	"testing"
)

// netExistingStates returns AWS layer states with $10/hr existing on the
// ComputeSP layer, $4/hr of which expires this cadence window, plus $2/hr on
// the EC2InstanceSP layer with nothing expiring. Gross existing is $12/hr and
// net existing (what Allocate subtracts from the target) is $8/hr.
func netExistingStates(layers []LayerSpec) map[LayerType]LayerState {
	states := zeroStates(layers)
	states = withExisting(states, LayerComputeSP, 10.0)
	states = withExpiring(states, LayerComputeSP, 4.0)
	return withExisting(states, LayerEC2InstanceSP, 2.0)
}

// TestAllocate_ReportsNetExisting verifies the result carries the net
// existing commitment (existing - expiring, summed over layers) that Allocate
// used to size the gap, on every successful return path, so callers can
// report the same figure instead of recomputing a gross one (go#83).
func TestAllocate_ReportsNetExisting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lowWater  *float64
		name      string
		wantAlloc bool
	}{
		// target $20 > net $8: purchases are planned.
		{name: "allocation", lowWater: ptr(20.0), wantAlloc: true},
		// target $8 == net $8: target-met hold, nothing bought.
		{name: "target met hold", lowWater: ptr(8.0)},
		// no baseline: baseline-unavailable hold.
		{name: "baseline unavailable hold", lowWater: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			layers := awsLayers()
			result, err := Allocate(&AllocationInput{
				Config:          validConfigAWS(),
				Layers:          layers,
				Baseline:        UsageBaseline{LowWaterUSDPerHour: tc.lowWater},
				LayerStates:     netExistingStates(layers),
				Now:             nowFixed(),
				InFlight:        noInFlight(),
				CommitmentRates: unitRates(),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.NetExistingUSDPerHour == nil {
				t.Fatal("NetExistingUSDPerHour is nil; want the net existing commitment Allocate used")
			}
			ratEq(t, "NetExistingUSDPerHour", result.NetExistingUSDPerHour, 8, 1)
			if got := len(result.Allocations) > 0; got != tc.wantAlloc {
				t.Fatalf("allocations present = %v, want %v (holds: %+v)", got, tc.wantAlloc, result.Holds)
			}
		})
	}
}

// TestAllocate_NetExistingReconcilesWithGap verifies that the reported net
// existing figure is the E the gap was sized against: with unit rates and
// 100% coverage, target - NetExistingUSDPerHour equals the sum of the
// planned on-demand-equivalent gaps. A gross figure ($12/hr here) would not
// reconcile, which is the approval-email inconsistency go#83 describes.
func TestAllocate_NetExistingReconcilesWithGap(t *testing.T) {
	t.Parallel()
	layers := awsLayers()
	result, err := Allocate(&AllocationInput{
		Config:          validConfigAWS(),
		Layers:          layers,
		Baseline:        UsageBaseline{LowWaterUSDPerHour: ptr(20.0)},
		LayerStates:     netExistingStates(layers),
		Now:             nowFixed(),
		InFlight:        noInFlight(),
		CommitmentRates: unitRates(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NetExistingUSDPerHour == nil {
		t.Fatal("NetExistingUSDPerHour is nil")
	}
	planned := new(big.Rat)
	for _, a := range result.Allocations {
		planned.Add(planned, a.OnDemandEquivalentUSDPerHour)
	}
	reconciled := new(big.Rat).Add(planned, result.NetExistingUSDPerHour)
	ratEq(t, "planned gap + NetExistingUSDPerHour", reconciled, 20, 1)
}

// TestAllocate_NilBaselineInconsistentSnapshot_Error verifies that net
// existing is computed (and an inconsistent snapshot rejected) before the
// baseline-unavailable hold, so the hold never reports an existing figure
// derived from expiring > existing.
func TestAllocate_NilBaselineInconsistentSnapshot_Error(t *testing.T) {
	t.Parallel()
	layers := awsLayers()
	states := withExisting(zeroStates(layers), LayerConvertibleRI, 2.0)
	states = withExpiring(states, LayerConvertibleRI, 5.0)
	_, err := Allocate(&AllocationInput{
		Config:          validConfigAWS(),
		Layers:          layers,
		Baseline:        UsageBaseline{},
		LayerStates:     states,
		Now:             nowFixed(),
		InFlight:        noInFlight(),
		CommitmentRates: unitRates(),
	})
	if err == nil {
		t.Fatal("expected an error for expiring > existing with a nil baseline, got nil")
	}
	if !strings.Contains(err.Error(), "inconsistent provider snapshot") {
		t.Errorf("error missing %q: %v", "inconsistent provider snapshot", err)
	}
}
