package computeengine

import (
	"context"
	"testing"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func planClient(t *testing.T, recs ...*recommenderpb.Recommendation) (*Client, *MockCommitmentsService) {
	t.Helper()
	client, err := NewClient(context.Background(), "test-project", "us-central1")
	require.NoError(t, err)
	client.SetBillingService(&MockBillingService{err: assert.AnError})
	client.SetRecommenderClient(&MockRecommenderClient{iterator: &MockRecommenderIterator{recommendations: recs}})
	service := &MockCommitmentsService{operation: &MockOperation{}}
	client.SetCommitmentsService(service)
	return client, service
}

// Issue #291: a THIRTY_SIX_MONTH recommendation with no caller term must be
// bought as THIRTY_SIX_MONTH. Pre-fix it was labelled 1yr and bought TWELVE_MONTH.
func TestRecommendationPlanDrivesPurchaseTerm(t *testing.T) {
	cases := []struct {
		name, plan, paramTerm, wantTerm, wantPlan string
	}{
		{"36mo plan, empty term", "THIRTY_SIX_MONTH", "", "3yr", "THIRTY_SIX_MONTH"},
		{"12mo plan, empty term", "TWELVE_MONTH", "", "1yr", "TWELVE_MONTH"},
		{"36mo plan, 3yr", "THIRTY_SIX_MONTH", "3yr", "3yr", "THIRTY_SIX_MONTH"},
		{"36mo plan, 36mo spelling", "THIRTY_SIX_MONTH", "36mo", "3yr", "THIRTY_SIX_MONTH"},
		{"36mo plan, bare 3", "THIRTY_SIX_MONTH", " 3 ", "3yr", "THIRTY_SIX_MONTH"},
		{"no plan, explicit 3yr", "", "3yr", "3yr", "THIRTY_SIX_MONTH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, service := planClient(t, rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", tc.plan))
			recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{Term: tc.paramTerm})
			require.NoError(t, err)
			require.Len(t, recs, 1)
			assert.Equal(t, tc.wantTerm, recs[0].Term)
			_, err = client.PurchaseCommitment(context.Background(), recs[0], common.PurchaseOptions{})
			require.NoError(t, err)
			assert.Equal(t, tc.wantPlan, service.lastInsertReq.CommitmentResource.GetPlan())
		})
	}
}

func TestRecommendationPlanSkips(t *testing.T) {
	malformed := func(v *structpb.Value) *recommenderpb.Recommendation {
		rec := rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", "")
		rec.Content.OperationGroups[0].Operations[0].GetValue().GetStructValue().Fields["plan"] = v
		return rec
	}
	cases := map[string]struct {
		rec  *recommenderpb.Recommendation
		term string
	}{
		"conflict 12mo vs 3yr":  {rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", "TWELVE_MONTH"), "3yr"},
		"conflict 36mo vs 1yr":  {rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", "THIRTY_SIX_MONTH"), "1yr"},
		"missing plan, no term": {rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", ""), ""},
		"UNDEFINED_PLAN":        {malformed(structpb.NewStringValue("UNDEFINED_PLAN")), ""},
		"unknown plan":          {malformed(structpb.NewStringValue("FIVE_YEAR")), ""},
		"empty plan":            {malformed(structpb.NewStringValue("")), ""},
		"numeric plan":          {malformed(structpb.NewNumberValue(36)), ""},
		"null plan":             {malformed(structpb.NewNullValue()), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client, service := planClient(t, tc.rec)
			recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{Term: tc.term})
			require.NoError(t, err)
			assert.Empty(t, recs)
			assert.Empty(t, service.insertReqs)
		})
	}
}

// A conflicting recommendation is skipped without dropping its good neighbour.
func TestRecommendationPlanConflictKeepsGoodRecommendation(t *testing.T) {
	bad := rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", "TWELVE_MONTH")
	bad.Name = "bad"
	good := rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", "THIRTY_SIX_MONTH")
	good.Name = "good"
	client, service := planClient(t, bad, good)
	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{Term: "3yr"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "3yr", recs[0].Term)
	assert.Empty(t, service.insertReqs, "listing must not insert")
}

// Mutation probe: the plan-to-term mapping must not be swappable.
func TestRootCommitmentTermMapping(t *testing.T) {
	for plan, want := range map[string]string{"TWELVE_MONTH": "1yr", "THIRTY_SIX_MONTH": "3yr"} {
		got, found, err := rootCommitmentTerm(rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", plan).GetContent())
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, want, got)
	}
}

// An explicit non-canonical Term must be canonicalised so pricing and grouping
// see "3yr", not "36mo" (which GetOfferingDetails would price as one year).
func TestExplicitTermIsCanonicalised(t *testing.T) {
	for _, raw := range []string{"36mo", " 3 ", "3yr"} {
		for _, plan := range []string{"", "THIRTY_SIX_MONTH"} {
			client, _ := planClient(t, rootCUDRecommendationWithPlan("MEMORY_OPTIMIZED_M4_6TB", plan))
			recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{Term: raw})
			require.NoError(t, err)
			require.Len(t, recs, 1)
			assert.Equal(t, "3yr", recs[0].Term, "raw %q plan %q", raw, plan)
			assert.Equal(t, 3, termYearsFromTerm(recs[0].Term))
		}
	}
	assert.Equal(t, "3yr", canonicalTerm("36mo"))
}
