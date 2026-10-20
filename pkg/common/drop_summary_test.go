package common //nolint:revive // pkg/common is the module's core domain package (Recommendation, ProviderType, ...), imported by name (common.X) throughout this repo and by consumer-mcp; renaming would break every consumer import

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDropSummary_Add_And_Total(t *testing.T) {
	t.Parallel()
	d := NewDropSummary()
	assert.Equal(t, 0, d.Total())
	assert.True(t, d.IsEmpty())

	d.Add(DropMinPoolSize, 8)
	d.Add(DropTargetAlreadyMet, 4)
	d.Add(DropDuplicateDedup, 2)

	assert.Equal(t, 14, d.Total())
	assert.False(t, d.IsEmpty())
}

func TestDropSummary_Add_Zero_NoOp(t *testing.T) {
	t.Parallel()
	d := NewDropSummary()
	d.Add(DropMinPoolSize, 0)
	assert.True(t, d.IsEmpty())
}

func TestDropSummary_NilReceiver_Safe(t *testing.T) {
	t.Parallel()
	var d *DropSummary
	d.Add(DropMinPoolSize, 3) // must not panic
	assert.Equal(t, 0, d.Total())
	assert.True(t, d.IsEmpty())
	assert.Equal(t, "", d.FormatOneLine())
}

// Regression: a zero-value DropSummary (declared without NewDropSummary)
// must not panic on Add because the internal map is lazily initialized.
func TestDropSummary_ZeroValue_Safe(t *testing.T) {
	t.Parallel()
	var d DropSummary
	d.Add(DropMinPoolSize, 3) // must not panic on nil map
	d.Add(DropTargetAlreadyMet, 2)
	assert.Equal(t, 5, d.Total())
	assert.False(t, d.IsEmpty())
	assert.Equal(t, "Dropped 5 recs: --min-pool-size=3, target-already-met=2", d.FormatOneLine())
}

func TestDropSummary_FormatOneLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(*DropSummary)
		expected string
	}{
		{
			name:     "empty returns blank string",
			setup:    func(_ *DropSummary) {},
			expected: "",
		},
		{
			name: "single reason",
			setup: func(d *DropSummary) {
				d.Add(DropMinPoolSize, 3)
			},
			expected: "Dropped 3 recs: --min-pool-size=3",
		},
		{
			name: "multiple reasons sorted deterministically",
			setup: func(d *DropSummary) {
				d.Add(DropMinPoolSize, 8)
				d.Add(DropTargetAlreadyMet, 4)
				d.Add(DropDuplicateDedup, 2)
			},
			// Alphabetically: --min-pool-size, duplicate-dedup, target-already-met
			expected: "Dropped 14 recs: --min-pool-size=8, duplicate-dedup=2, target-already-met=4",
		},
		{
			name: "all drop categories present",
			setup: func(d *DropSummary) {
				d.Add(DropMinPoolSize, 1)
				d.Add(DropExtendedSupport, 2)
				d.Add(DropTargetAlreadyMet, 3)
				d.Add(DropTargetSizedToZero, 4)
				d.Add(DropFamilyAlreadyAtTarget, 5)
				d.Add(DropFamilyNoNUSignal, 6)
				d.Add(DropFamilySizedToZero, 7)
				d.Add(DropDuplicateDedup, 8)
			},
			expected: "Dropped 36 recs: --include-extended-support=2, --min-pool-size=1, " +
				"duplicate-dedup=8, family-nu-already-at-target=5, family-nu-no-nu-signal=6, " +
				"family-nu-sized-to-zero=7, target-already-met=3, target-sized-to-zero=4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewDropSummary()
			tt.setup(d)
			assert.Equal(t, tt.expected, d.FormatOneLine())
		})
	}
}

func TestDropSummary_Add_PerReasonTallies(t *testing.T) {
	t.Parallel()
	d := NewDropSummary()
	d.Add(DropMinPoolSize, 3)
	d.Add(DropMinPoolSize, 2)
	d.Add(DropDuplicateDedup, 4)
	d.Add(DropTargetAlreadyMet, 0)

	assert.Equal(t, 9, d.Total())
	assert.Equal(t, map[string]int{DropMinPoolSize: 5, DropDuplicateDedup: 4}, d.counts)
}

func TestDropSummary_Add_NegativePanics(t *testing.T) {
	t.Parallel()
	d := NewDropSummary()
	assert.PanicsWithValue(t,
		`common.DropSummary.Add: negative count -1 for reason "--min-pool-size"`,
		func() { d.Add(DropMinPoolSize, -1) })
	assert.True(t, d.IsEmpty(), "a rejected Add must not touch the tally")
}

// A negative that would cancel a positive in Total() is the case the guard
// exists for: Total() alone would read 0 and look correct.
func TestDropSummary_Add_NegativeCannotCancelInTotal(t *testing.T) {
	t.Parallel()
	d := NewDropSummary()
	d.Add(DropMinPoolSize, 2)
	d.Add(DropDuplicateDedup, 5)
	assert.Panics(t, func() { d.Add(DropDuplicateDedup, -5) })
	assert.Panics(t, func() { d.Add(DropMinPoolSize, -2) })

	assert.Equal(t, 7, d.Total())
	assert.Equal(t, map[string]int{DropMinPoolSize: 2, DropDuplicateDedup: 5}, d.counts)
}

func TestDropSummary_Add_NegativePanicsOnNilReceiver(t *testing.T) {
	t.Parallel()
	var d *DropSummary
	assert.Panics(t, func() { d.Add(DropMinPoolSize, -1) })
}
