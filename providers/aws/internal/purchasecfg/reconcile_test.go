package purchasecfg

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	wantLo, wantHi := start.Add(-ClockSkew), end.Add(ClockSkew)

	tests := []struct {
		name       string
		start, end time.Time
		ok         bool
	}{
		{"valid", start, end, true},
		{"equal start and end", start, start, true},
		{"zero start", time.Time{}, end, false},
		{"zero end", start, time.Time{}, false},
		{"both zero", time.Time{}, time.Time{}, false},
		{"end before start", end, start, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lo, hi, ok := ReconcileWindow(tt.start, tt.end)
			assert.Equal(t, tt.ok, ok)
			if !tt.ok {
				assert.True(t, lo.IsZero() && hi.IsZero())
			}
		})
	}

	lo, hi, ok := ReconcileWindow(start, end)
	require.True(t, ok)
	assert.Equal(t, wantLo, lo)
	assert.Equal(t, wantHi, hi)
}

func TestInWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	lo, hi, ok := ReconcileWindow(start, end)
	require.True(t, ok)

	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"exactly lo", lo, true},
		{"one ns before lo", lo.Add(-time.Nanosecond), false},
		{"inside", start, true},
		{"exactly hi", hi, true},
		{"one ns after hi", hi.Add(time.Nanosecond), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, InWindow(tt.at, lo, hi))
		})
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	crit := []string{"instance-type", "count"}

	tests := []struct {
		name       string
		matches    []string
		uncertain  []string
		tagged     bool
		verdict    common.ReconcileVerdict
		id, reason string
		candidates []string
	}{
		{"0 match 0 uncertain", nil, nil, false, common.ReconcileUnknown, "", ReasonNoCandidate, nil},
		{"0 match 1 uncertain", nil, []string{"u1"}, false, common.ReconcileUnknown, "", ReasonUncertainCandidate, []string{"u1"}},
		{"1 match 0 uncertain", []string{"m1"}, nil, false, common.ReconcileFound, "m1", "", nil},
		{"1 match 0 uncertain tagged", []string{"m1"}, nil, true, common.ReconcileFound, "m1", "", nil},
		{"1 match 1 uncertain", []string{"m1"}, []string{"u1"}, false, common.ReconcileUnknown, "", ReasonUncertainCandidate, []string{"m1", "u1"}},
		{"1 empty-id match", []string{""}, nil, false, common.ReconcileUnknown, "", ReasonUncertainCandidate, nil},
		{"2 match one empty id", []string{"m1", ""}, nil, false, common.ReconcileUnknown, "", ReasonUncertainCandidate, []string{"m1"}},
		{"2 match 0 uncertain", []string{"m1", "m2"}, nil, false, common.ReconcileUnknown, "", ReasonAmbiguous, []string{"m1", "m2"}},
		{"2 match 1 uncertain", []string{"m1", "m2"}, []string{"u1"}, false, common.ReconcileUnknown, "", ReasonUncertainCandidate, []string{"m1", "m2", "u1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tt.matches, tt.uncertain, crit, tt.tagged)
			assert.Equal(t, tt.verdict, got.Verdict)
			assert.Equal(t, tt.id, got.CommitmentID)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, tt.candidates, got.CandidateIDs)
			if tt.verdict == common.ReconcileFound {
				assert.True(t, got.IsFound())
				assert.Equal(t, crit, got.Criteria)
				assert.Equal(t, tt.tagged, got.Tagged)
				assert.Empty(t, got.Reason)
			} else {
				assert.False(t, got.IsFound())
				assert.Empty(t, got.CommitmentID)
				assert.Empty(t, got.Criteria)
				assert.False(t, got.Tagged)
			}
		})
	}
}

func TestUnknownResult(t *testing.T) {
	t.Parallel()
	got := UnknownResult(ReasonLookupFailed)
	assert.Equal(t, common.ReconcileUnknown, got.Verdict)
	assert.Equal(t, ReasonLookupFailed, got.Reason)
	assert.Empty(t, got.CommitmentID)
	assert.False(t, got.IsFound())
}

func TestReasonPhrasesAreDistinct(t *testing.T) {
	t.Parallel()
	phrases := []string{
		ReasonInvalidWindow, ReasonInvalidRequest, ReasonLookupFailed, ReasonLookupIncomplete,
		ReasonOfferingUnresolved, ReasonNoCandidate, ReasonAmbiguous, ReasonUncertainCandidate,
	}
	seen := map[string]bool{}
	for _, p := range phrases {
		assert.NotEmpty(t, p)
		assert.False(t, seen[p], "duplicate reason phrase %q", p)
		seen[p] = true
	}
	assert.Len(t, seen, 8)
}
