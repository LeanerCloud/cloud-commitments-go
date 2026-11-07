package ladder

import "testing"

// #554: writers emit the US spelling "canceled"; rows written before keep the
// legacy "cancelled" and still parse, validate and count as terminal.

func TestCanceledStatuses_WriteTheUSSpelling(t *testing.T) {
	t.Parallel()
	if RunStatusCanceled != "canceled" || TrancheStatusCanceled != "canceled" {
		t.Fatalf("writers must emit \"canceled\": run %q, tranche %q", RunStatusCanceled, TrancheStatusCanceled)
	}
	if RunStatusCancelled != RunStatusCanceled || TrancheStatusCancelled != TrancheStatusCanceled {
		t.Fatal("the deprecated Cancelled aliases must equal the Canceled constants")
	}
}

func TestCanceledStatuses_LegacySpellingParsesToCanceled(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"canceled", "cancelled"} {
		run, err := ParseRunStatus(in)
		if err != nil || run != RunStatusCanceled {
			t.Errorf("ParseRunStatus(%q) = %q, %v; want %q", in, run, err, RunStatusCanceled)
		}
		tr, err := ParseTrancheStatus(in)
		if err != nil || tr != TrancheStatusCanceled {
			t.Errorf("ParseTrancheStatus(%q) = %q, %v; want %q", in, tr, err, TrancheStatusCanceled)
		}
		if err := RunStatus(in).Validate(); err != nil {
			t.Errorf("RunStatus(%q).Validate() = %v; a stored legacy row must still validate", in, err)
		}
		if err := TrancheStatus(in).Validate(); err != nil {
			t.Errorf("TrancheStatus(%q).Validate() = %v", in, err)
		}
		if !RunStatus(in).IsTerminal() {
			t.Errorf("RunStatus(%q).IsTerminal() = false", in)
		}
		if TrancheStatus(in).allowsFiredAt() {
			t.Errorf("TrancheStatus(%q) must not allow FiredAt", in)
		}
	}
}
