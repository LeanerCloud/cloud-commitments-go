package gcp

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestGetRecommendations_PaymentOptionValidatedBeforeRegions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		option  string
		wantErr bool
	}{
		{"empty", "", false},
		{"monthly", "monthly", false},
		{"all-upfront", "all-upfront", false},
		{"wrong case", "MONTHLY", false},
		{"unknown", "bogus", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			adapter := &RecommendationsClientAdapter{
				projectID: "test-project",
				regionsFn: func(context.Context) ([]string, error) {
					calls.Add(1)
					return nil, nil
				},
			}
			recs, err := adapter.GetRecommendations(context.Background(), &common.RecommendationParams{PaymentOption: tc.option})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, recs)
				assert.Zero(t, calls.Load())
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}
