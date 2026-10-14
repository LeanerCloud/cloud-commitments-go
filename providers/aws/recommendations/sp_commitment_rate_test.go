package recommendations

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

const lookback30dHours = 720.0

// rateDetail builds a constructed CE-shaped detail. All amounts are lookback
// totals as CE returns them.
func rateDetail(acct, region, spCost, savings, util string) types.SavingsPlansPurchaseRecommendationDetail {
	d := types.SavingsPlansPurchaseRecommendationDetail{
		AccountId:                   aws.String(acct),
		EstimatedSPCost:             aws.String(spCost),
		EstimatedSavingsAmount:      aws.String(savings),
		EstimatedAverageUtilization: aws.String(util),
		// Decoy: measured against ALL on-demand spend; must not drive r.
		EstimatedSavingsPercentage: aws.String("18"),
	}
	if region != "" {
		d.SavingsPlansDetails = &types.SavingsPlansDetails{Region: aws.String(region)}
	}
	return d
}

func rateClient(details ...types.SavingsPlansPurchaseRecommendationDetail) *Client {
	return NewClientWithAPI(&mockCostExplorerAPI{
		spRecommendations: &costexplorer.GetSavingsPlansPurchaseRecommendationOutput{
			SavingsPlansPurchaseRecommendation: &types.SavingsPlansPurchaseRecommendation{
				SavingsPlansPurchaseRecommendationDetails: details,
			},
		},
	}, "us-east-1")
}

func rateParams(pt types.SupportedSavingsPlansType) SPCommitmentRateParams {
	return SPCommitmentRateParams{PlanType: pt, AccountID: "acct-a", Region: "us-east-1",
		Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "30d"}
}

func TestGetSPCommitmentRate_FixtureCrossCheck(t *testing.T) {
	// OD 1.00/hr over 720h = 720; CE says 300 stays on-demand with the SP, so
	// covered_od = 420 = SPCost 294 + savings 126: r = 0.7 at full utilization.
	// The 18% savings-vs-all-OD decoy would give 0.82 (the rejected formula).
	currentOD := 1.0 * lookback30dHours
	remainingOD := 300.0
	d := rateDetail("acct-a", "US East (N. Virginia)", "294", "126", "100")
	d.CurrentAverageHourlyOnDemandSpend = aws.String("1.0")
	d.EstimatedOnDemandCost = aws.String(strconv.FormatFloat(remainingOD, 'f', -1, 64))
	if got := currentOD - remainingOD; got != 420 {
		t.Fatalf("fixture inconsistent: covered_od cross-check %g != 420", got)
	}
	r, err := rateClient(d).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r-0.7) > 1e-12 {
		t.Fatalf("r = %g, want 0.7", r)
	}
}

func TestGetSPCommitmentRate_UtilizationScalesUsedSpend(t *testing.T) {
	d := rateDetail("acct-a", "US East (N. Virginia)", "294", "126", "80")
	r, err := rateClient(d).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
	if err != nil || math.Abs(r-0.56) > 1e-12 {
		t.Fatalf("r = %g, err %v, want 0.56", r, err)
	}
}

func TestGetSPCommitmentRate_RatioOfSumsNotMeanOfRatios(t *testing.T) {
	// r1 = 0.5 (covered 20), r2 = 0.8 (covered 100): mean 0.65, ratio of sums
	// (10+80)/120 = 0.75.
	a := rateDetail("acct-a", "US East (N. Virginia)", "10", "10", "100")
	b := rateDetail("acct-a", "US East (N. Virginia)", "80", "20", "100")
	r, err := rateClient(a, b).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
	if err != nil || math.Abs(r-0.75) > 1e-12 {
		t.Fatalf("r = %g, err %v, want 0.75", r, err)
	}
}

func TestGetSPCommitmentRate_FiltersAccountAndRegion(t *testing.T) {
	wrongAcct := rateDetail("acct-b", "US East (N. Virginia)", "10", "90", "100")
	wrongRegion := rateDetail("acct-a", "EU (Ireland)", "10", "90", "100")
	right := rateDetail("acct-a", "US East (N. Virginia)", "70", "30", "100")
	r, err := rateClient(wrongAcct, wrongRegion, right).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
	if err != nil || math.Abs(r-0.7) > 1e-12 {
		t.Fatalf("r = %g, err %v, want 0.7", r, err)
	}
}

func TestGetSPCommitmentRate_ComputeSPIgnoresRegion(t *testing.T) {
	d := rateDetail("acct-a", "", "60", "40", "100")
	r, err := rateClient(d).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeComputeSp))
	if err != nil || math.Abs(r-0.6) > 1e-12 {
		t.Fatalf("r = %g, err %v, want 0.6", r, err)
	}
}

func TestGetSPCommitmentRate_TypedAbsence(t *testing.T) {
	missing := func(mut func(*types.SavingsPlansPurchaseRecommendationDetail)) types.SavingsPlansPurchaseRecommendationDetail {
		d := rateDetail("acct-a", "US East (N. Virginia)", "70", "30", "100")
		mut(&d)
		return d
	}
	cases := map[string][]types.SavingsPlansPurchaseRecommendationDetail{
		"no details":         nil,
		"no matching detail": {rateDetail("acct-b", "US East (N. Virginia)", "70", "30", "100")},
		"missing SP cost":    {missing(func(d *types.SavingsPlansPurchaseRecommendationDetail) { d.EstimatedSPCost = nil })},
		"missing savings":    {missing(func(d *types.SavingsPlansPurchaseRecommendationDetail) { d.EstimatedSavingsAmount = nil })},
		"missing util":       {missing(func(d *types.SavingsPlansPurchaseRecommendationDetail) { d.EstimatedAverageUtilization = nil })},
		"zero covered":       {rateDetail("acct-a", "US East (N. Virginia)", "0", "0", "100")},
	}
	for name, details := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := rateClient(details...).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
			if !errors.Is(err, ErrNoSPCommitmentRate) || r != 0 {
				t.Fatalf("r = %g, err %v, want ErrNoSPCommitmentRate", r, err)
			}
		})
	}
}

func TestGetSPCommitmentRate_InvalidFieldsAreErrors(t *testing.T) {
	for name, d := range map[string]types.SavingsPlansPurchaseRecommendationDetail{
		"unparseable": rateDetail("acct-a", "US East (N. Virginia)", "abc", "30", "100"),
		"NaN":         rateDetail("acct-a", "US East (N. Virginia)", "NaN", "30", "100"),
		"negative":    rateDetail("acct-a", "US East (N. Virginia)", "70", "-0.5", "100"),
		"util > 100":  rateDetail("acct-a", "US East (N. Virginia)", "70", "30", "101"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rateClient(d).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
			if err == nil || errors.Is(err, ErrNoSPCommitmentRate) {
				t.Fatalf("want a non-absence error, got %v", err)
			}
		})
	}
}

func TestGetSPCommitmentRate_BadParams(t *testing.T) {
	c := rateClient()
	for name, mut := range map[string]func(*SPCommitmentRateParams){
		"plan type": func(p *SPCommitmentRateParams) { p.PlanType = "SAGEMAKER_SP" },
		"account":   func(p *SPCommitmentRateParams) { p.AccountID = "" },
		"region":    func(p *SPCommitmentRateParams) { p.Region = "" },
		"term":      func(p *SPCommitmentRateParams) { p.Term = "2yr" },
		"payment":   func(p *SPCommitmentRateParams) { p.PaymentOption = "x" },
		"lookback":  func(p *SPCommitmentRateParams) { p.LookbackPeriod = "9d" },
	} {
		t.Run(name, func(t *testing.T) {
			p := rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp)
			mut(&p)
			if _, err := c.GetSPCommitmentRate(context.Background(), p); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestGetSPCommitmentRate_SDKPageCompleteness(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"absent-first", "absent-later", "empty-first", "empty-middle"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := &spSDKFixture{respond: func(in spSDKRequest) (map[string]any, error) {
				if kind == "absent-first" || (kind == "absent-later" && in.NextPageToken == "next") {
					return map[string]any{}, nil
				}
				if kind == "empty-first" && in.NextPageToken == "" {
					return spSDKPage("next"), nil
				}
				if kind == "empty-middle" && in.NextPageToken == "next" {
					return spSDKPage("last"), nil
				}
				token := ""
				if in.NextPageToken == "" {
					token = "next"
				}
				return spSDKPage(token, map[string]any{
					"AccountId": "acct-a", "EstimatedSPCost": "70", "EstimatedSavingsAmount": "30",
					"EstimatedAverageUtilization": "100", "SavingsPlansDetails": map[string]any{"Region": "us-east-1"},
				}), nil
			}}
			rate, err := spSDKClient(fixture).GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
			if kind == "absent-first" || kind == "absent-later" {
				if !errors.Is(err, ErrNoSPCommitmentRate) || rate != 0 {
					t.Fatalf("rate = %g, err %v, want ErrNoSPCommitmentRate", rate, err)
				}
			} else if err != nil || math.Abs(rate-0.7) > 1e-12 {
				t.Fatalf("rate = %g, err %v, want 0.7", rate, err)
			}
			wantTokens := []string{"", "next"}
			if kind == "absent-first" {
				wantTokens = wantTokens[:1]
			} else if kind == "empty-middle" {
				wantTokens = append(wantTokens, "last")
			}
			if len(fixture.requests) != len(wantTokens) {
				t.Fatalf("requests = %v, want tokens %v", fixture.requests, wantTokens)
			}
			for i, token := range wantTokens {
				if fixture.requests[i].NextPageToken != token {
					t.Fatalf("request %d token = %q, want %q", i, fixture.requests[i].NextPageToken, token)
				}
			}
		})
	}
}

func TestGetSPCommitmentRate_NilOutput(t *testing.T) {
	c := NewClientWithAPI(&mockCostExplorerAPI{}, "us-east-1")
	rate, err := c.GetSPCommitmentRate(context.Background(), rateParams(types.SupportedSavingsPlansTypeEc2InstanceSp))
	if !errors.Is(err, ErrNoSPCommitmentRate) || rate != 0 {
		t.Fatalf("rate = %g, err %v, want ErrNoSPCommitmentRate", rate, err)
	}
}
