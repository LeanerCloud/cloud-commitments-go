package reccontract

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestCheckAvailable(t *testing.T) {
	t.Parallel()
	const reason = "no recommendations API"
	listed := common.ServiceType("svc-listed")
	caps := Capabilities{
		Provider: common.ProviderType("prov"),
		// The "" entry proves the empty-service guard, not a missing key, returns nil.
		Unavailable: map[common.ServiceType]string{listed: reason, "": reason},
	}
	tests := []struct {
		name    string
		caps    Capabilities
		service common.ServiceType
		wantErr bool
	}{
		{"empty service", caps, "", false},
		{"listed service", caps, listed, true},
		{"unlisted service", caps, "svc-other", false},
		{"nil map", Capabilities{Provider: "prov"}, listed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckAvailable(tt.caps, tt.service)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("errors.Is(ErrUnavailable) = false for %v", err)
			}
			var ue *UnavailableError
			if !errors.As(err, &ue) {
				t.Fatalf("errors.As *UnavailableError failed for %v", err)
			}
			if ue.Reason != reason || ue.Provider != "prov" || ue.Service != listed {
				t.Fatalf("unexpected fields: %+v", ue)
			}
		})
	}
}

func TestUnavailableErrorMessage(t *testing.T) {
	t.Parallel()
	err := &UnavailableError{Provider: "prov", Service: "svc", Reason: "because"}
	for _, want := range []string{"prov", "svc", "because"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, missing %q", err.Error(), want)
		}
	}
}

func TestUnavailableErrorIsWrapped(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("fetch: %w", &UnavailableError{Provider: "prov", Service: "svc", Reason: "r"})
	if !errors.Is(wrapped, ErrUnavailable) {
		t.Fatal("errors.Is through %w wrapping = false")
	}
	if errors.Is(errors.New("other"), ErrUnavailable) {
		t.Fatal("unrelated error matched ErrUnavailable")
	}
}
