package common

import "testing"

func TestReconcileResultIsFound(t *testing.T) {
	tests := []struct {
		name string
		r    ReconcileResult
		want bool
	}{
		{"zero value", ReconcileResult{}, false},
		{"found with id", ReconcileResult{Verdict: ReconcileFound, CommitmentID: "ri-1"}, true},
		{"found without id", ReconcileResult{Verdict: ReconcileFound}, false},
		{"unknown with id", ReconcileResult{Verdict: ReconcileUnknown, CommitmentID: "ri-1"}, false},
		{"unknown without id", ReconcileResult{Verdict: ReconcileUnknown, Reason: "no-candidate"}, false},
		{"unrecognized verdict with id", ReconcileResult{Verdict: "not-found", CommitmentID: "ri-1"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.IsFound(); got != tc.want {
				t.Fatalf("IsFound() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileVerdictValues(t *testing.T) {
	if ReconcileFound != "found" || ReconcileUnknown != "unknown" {
		t.Fatalf("verdict values changed: %q %q", ReconcileFound, ReconcileUnknown)
	}
	if (ReconcileResult{}).Verdict != "" {
		t.Fatal("zero Verdict must stay empty so it is never Found")
	}
}
