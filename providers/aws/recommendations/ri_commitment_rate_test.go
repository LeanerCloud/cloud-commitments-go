package recommendations

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// riRateDetail builds a constructed CE-shaped EC2 detail. Cost fields are monthly
// USD for the whole recommended quantity; an empty string leaves the field nil.
func riRateDetail(acct, region, recurring, upfront, savings, util string) types.ReservationPurchaseRecommendationDetail {
	optional := func(s string) *string {
		if s == "" {
			return nil
		}
		return aws.String(s)
	}
	return types.ReservationPurchaseRecommendationDetail{
		AccountId:                     aws.String(acct),
		RecurringStandardMonthlyCost:  optional(recurring),
		UpfrontCost:                   optional(upfront),
		EstimatedMonthlySavingsAmount: optional(savings),
		AverageUtilization:            optional(util),
		// Decoys: neither may drive r.
		EstimatedMonthlySavingsPercentage: aws.String("18"),
		EstimatedMonthlyOnDemandCost:      aws.String("5000"),
		InstanceDetails: &types.InstanceDetails{EC2InstanceDetails: &types.EC2InstanceDetails{
			Region: aws.String(region), InstanceType: aws.String("m5.large"),
		}},
	}
}

func riClientPages(pages ...[]types.ReservationPurchaseRecommendationDetail) *Client {
	recs := make([]types.ReservationPurchaseRecommendation, len(pages))
	for i, details := range pages {
		recs[i] = types.ReservationPurchaseRecommendation{RecommendationDetails: details}
	}
	return NewClientWithAPI(&mockCostExplorerAPI{
		riRecommendations: &costexplorer.GetReservationPurchaseRecommendationOutput{Recommendations: recs},
	}, "us-east-1")
}

func riClient(details ...types.ReservationPurchaseRecommendationDetail) *Client {
	return riClientPages(details)
}

func riParams(term, payment string) RICommitmentRateParams {
	return RICommitmentRateParams{AccountID: "acct-a", Region: "us-east-1",
		Term: term, PaymentOption: payment, LookbackPeriod: "30d"}
}

func requireRate(t *testing.T, c *Client, p RICommitmentRateParams, want float64) {
	t.Helper()
	got, err := c.GetRICommitmentRate(context.Background(), p)
	if err != nil {
		t.Fatalf("GetRICommitmentRate: %v", err)
	}
	if math.Abs(got-want) > 1e-4 {
		t.Fatalf("r = %v, want %v", got, want)
	}
}

func TestGetRICommitmentRate_Core(t *testing.T) {
	t.Parallel()
	// eff 700, covered 1000: r = 0.7. The 18% decoy would give 0.82, a rate
	// without savings 1.0.
	requireRate(t, riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")),
		riParams("1yr", "no-upfront"), 0.7)
	// Utilization scales only the numerator: 560/1000.
	requireRate(t, riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "80")),
		riParams("1yr", "no-upfront"), 0.56)
	// Utilization above 100 is an error, not a clamp.
	_, err := riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "101")).
		GetRICommitmentRate(context.Background(), riParams("1yr", "no-upfront"))
	if err == nil || errors.Is(err, ErrNoRICommitmentRate) {
		t.Fatalf("util 101: want a plain error, got %v", err)
	}
}

func TestGetRICommitmentRate_UpfrontAmortization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, term, payment, recurring, upfront string
	}{
		{"1yr partial", "1yr", "partial-upfront", "350", "4200"},
		{"1yr all", "1yr", "all-upfront", "", "8400"},
		{"3yr all", "3yr", "all-upfront", "", "25200"},
		{"3yr partial", "3yr", "partial-upfront", "350", "12600"},
		{"3 numeric", "3", "all-upfront", "", "25200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Every case amortizes to eff 700 against savings 300: r = 0.7.
			c := riClient(riRateDetail("acct-a", "us-east-1", tc.recurring, tc.upfront, "300", "100"))
			requireRate(t, c, riParams(tc.term, tc.payment), 0.7)
		})
	}
}

func TestGetRICommitmentRate_RatioOfSums(t *testing.T) {
	t.Parallel()
	// (100+800)/(200+1000) = 0.75; the mean of the per-detail ratios is 0.65.
	c := riClient(
		riRateDetail("acct-a", "us-east-1", "100", "", "100", "100"),
		riRateDetail("acct-a", "us-east-1", "800", "", "200", "100"),
	)
	requireRate(t, c, riParams("1yr", "no-upfront"), 0.75)
	// The same sum holds across result entries (details of two recommendations).
	c = riClientPages(
		[]types.ReservationPurchaseRecommendationDetail{riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")},
		[]types.ReservationPurchaseRecommendationDetail{riRateDetail("acct-a", "us-east-1", "300", "", "200", "100")},
	)
	requireRate(t, c, riParams("1yr", "no-upfront"), 1000.0/1500.0)
}

func TestGetRICommitmentRate_Filters(t *testing.T) {
	t.Parallel()
	good := riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")
	nilRegion := riRateDetail("acct-a", "us-east-1", "10", "", "90", "100")
	nilRegion.InstanceDetails.EC2InstanceDetails.Region = nil
	noInstance := riRateDetail("acct-a", "us-east-1", "10", "", "90", "100")
	noInstance.InstanceDetails = nil
	noEC2 := riRateDetail("acct-a", "us-east-1", "10", "", "90", "100")
	noEC2.InstanceDetails.EC2InstanceDetails = nil
	c := riClient(
		good,
		riRateDetail("acct-b", "us-east-1", "10", "", "90", "100"),
		riRateDetail("acct-a", "EU (Ireland)", "10", "", "90", "100"),
		nilRegion, noInstance, noEC2,
	)
	requireRate(t, c, riParams("1yr", "no-upfront"), 0.7)

	// A display-name region and a region code both match the code.
	requireRate(t, riClient(riRateDetail("acct-a", "US East (N. Virginia)", "700", "", "300", "100")),
		riParams("1yr", "no-upfront"), 0.7)
	requireRate(t, riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")),
		riParams("1yr", "no-upfront"), 0.7)
	got, err := riClient(riRateDetail("acct-a", "EU (Ireland)", "700", "", "300", "100")).
		GetRICommitmentRate(context.Background(), riParams("1yr", "no-upfront"))
	if !errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
		t.Fatalf("other region only: r=%v err=%v, want the sentinel", got, err)
	}
}

func TestGetRICommitmentRate_NilOutputPage(t *testing.T) {
	t.Parallel()
	// Only a mock yields a nil output; the SDK always decodes a non-nil one.
	c := NewClientWithAPI(&mockCostExplorerAPI{}, "us-east-1")
	got, err := c.GetRICommitmentRate(context.Background(), riParams("1yr", "no-upfront"))
	if !errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
		t.Fatalf("nil output: r=%v err=%v, want the sentinel", got, err)
	}
}
