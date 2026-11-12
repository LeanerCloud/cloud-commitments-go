package computeengine

import (
	"context"
	"testing"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestConvertGCPRecommendation_PaymentOption(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		option  string
		wantErr bool
	}{
		{"empty", "", false},
		{"monthly", "monthly", false},
		{"no-upfront", "no-upfront", false},
		{"all-upfront forced to monthly", "all-upfront", false},
		{"partial-upfront forced to monthly", "partial-upfront", false},
		{"legacy upfront forced to monthly", "upfront", false},
		{"wrong case", "MONTHLY", false},
		{"unknown", "bogus", true},
		{"unknown spelling", "all_upfront", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := NewClient("test-project", "us-central1")
			rec, err := client.convertGCPRecommendation(context.Background(), &recommenderpb.Recommendation{Name: "test-rec"}, common.RecommendationParams{PaymentOption: tc.option})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, rec)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, rec)
			assert.Equal(t, "monthly", rec.PaymentOption)
		})
	}
}
