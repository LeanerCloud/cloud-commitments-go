package ladder

import (
	"math/big"
	"testing"
)

// A family whose stable base is already scheduled is not topped up again; the
// remaining gap goes to families with headroom, never past their own stable
// level.
func TestAllocate_FamilySplitNetsInFlightPerFamily(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.InFlight = []InFlightTranche{{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 3, Rate: 1}}
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	rows := baseRows(res)
	if len(rows) != 1 || rows[0].Family != "c5" {
		t.Fatalf("rows = %+v, want only c5", rows)
	}
	ratEq(t, "c5 gets its remaining headroom", rows[0].GapUSDPerHour, 1, 1)

	in = familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.InFlight = []InFlightTranche{
		{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 3, Rate: 1},
		{Layer: LayerEC2InstanceSP, Family: "c5", CommitmentUSDPerHour: 1, Rate: 1},
	}
	res, err = Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	if rows := baseRows(res); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none when every family is covered in flight", rows)
	}

	in = familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.InFlight = []InFlightTranche{{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 1, Rate: 0}}
	if _, err = Allocate(in); err == nil {
		t.Fatal("a family in-flight tranche without a valid rate must fail loud")
	}
}

// Shares that sum a hair under stable, with in-flight covering every share, must
// not fail the run: the base is capped at the headroom and the rest goes to flex.
func TestAllocate_FamilySplitCapsBaseAtHeadroom(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1 - 1e-9})
	in.InFlight = []InFlightTranche{
		{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 3, Rate: 1},
		{Layer: LayerEC2InstanceSP, Family: "c5", CommitmentUSDPerHour: 1, Rate: 0.999999999},
	}
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range baseRows(res) {
		if a.OnDemandEquivalentUSDPerHour.Sign() <= 0 {
			t.Fatalf("row %+v has no headroom", a)
		}
	}

	in = familyInput(map[string]float64{"m5": 3, "c5": 1 - 1e-9})
	in.InFlight = []InFlightTranche{{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 3 - 1e-12, Rate: 1}}
	res, err = Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	total := new(big.Rat)
	for _, a := range baseRows(res) {
		total.Add(total, a.OnDemandEquivalentUSDPerHour)
	}
	if limit := new(big.Rat).SetFloat64(1 - 1e-9 + 1e-12); total.Cmp(limit) > 0 {
		t.Fatalf("base rows total %s exceed the families' headroom", total.FloatString(12))
	}
}

// An over-covered family floors at zero headroom instead of going negative and
// pushing its negative weight onto the others.
func TestAllocate_FamilyHeadroomFloorsAtZero(t *testing.T) {
	t.Parallel()
	in := familyInput(map[string]float64{"m5": 3, "c5": 1})
	in.InFlight = []InFlightTranche{{Layer: LayerEC2InstanceSP, Family: "m5", CommitmentUSDPerHour: 3.5, Rate: 1}}
	res, err := Allocate(in)
	if err != nil {
		t.Fatal(err)
	}
	rows := baseRows(res)
	if len(rows) != 1 || rows[0].Family != "c5" {
		t.Fatalf("rows = %+v, want only c5", rows)
	}
	ratEq(t, "c5 gets the remaining 0.5", rows[0].GapUSDPerHour, 1, 2)
}
