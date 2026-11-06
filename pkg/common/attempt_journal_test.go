package common

import (
	"strings"
	"testing"
)

func TestAttemptRecordValidate(t *testing.T) {
	token := DeriveIdempotencyToken("exec-attempt-journal", 0)
	tests := []struct {
		name    string
		rec     AttemptRecord
		wantErr bool
	}{
		{"valid sending", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptSending, OfferingID: "o1"}, false},
		{"valid purchased", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptPurchased, ReservationID: "r1"}, false},
		{"valid outcome unknown", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptOutcomeUnknown}, false},
		{"empty token", AttemptRecord{ExecutionID: "e1", State: AttemptSending}, true},
		{"empty execution", AttemptRecord{IdempotencyToken: token, State: AttemptSending}, true},
		{"unknown state", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: "bogus"}, true},
		{"empty state", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1"}, true},
		{"purchased without reservation", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptPurchased}, true},
		{"sending with reservation", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptSending, ReservationID: "r1"}, true},
		{"outcome unknown with reservation", AttemptRecord{IdempotencyToken: token, ExecutionID: "e1", State: AttemptOutcomeUnknown, ReservationID: "r1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rec.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), token) {
				t.Fatalf("error leaks the full token: %v", err)
			}
		})
	}
}
