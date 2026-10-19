package recommendations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

type currencySDKFixture struct {
	t             *testing.T
	detail        map[string]any
	extra         []any
	payment, term string
	sp            bool
	calls         int
}

func (f *currencySDKFixture) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "ce.us-east-1.amazonaws.com" || req.Method != http.MethodPost {
		return nil, fmt.Errorf("currency fixture rejects request %s %s", req.Method, req.URL)
	}
	operation := "GetReservationPurchaseRecommendation"
	if f.sp {
		operation = "GetSavingsPlansPurchaseRecommendation"
	}
	if req.Header.Get("X-Amz-Target") != "AWSInsightsIndexService."+operation {
		return nil, fmt.Errorf("currency fixture rejects operation %s", req.Header.Get("X-Amz-Target"))
	}
	f.calls++
	var input map[string]any
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	payment := map[string]string{"all-upfront": "ALL_UPFRONT", "partial-upfront": "PARTIAL_UPFRONT", "no-upfront": "NO_UPFRONT"}[f.payment]
	if payment == "" {
		assert.Empty(f.t, input["PaymentOption"])
	} else {
		assert.Equal(f.t, payment, input["PaymentOption"])
	}
	assert.Equal(f.t, map[string]string{"1yr": "ONE_YEAR", "3yr": "THREE_YEARS"}[f.term], input["TermInYears"])
	assert.Equal(f.t, "SEVEN_DAYS", input["LookbackPeriodInDays"])
	assert.Equal(f.t, "LINKED", input["AccountScope"])
	var response map[string]any
	if f.sp {
		assert.Equal(f.t, "COMPUTE_SP", input["SavingsPlansType"])
		response = map[string]any{"SavingsPlansPurchaseRecommendation": map[string]any{
			"SavingsPlansPurchaseRecommendationDetails": append([]any{f.detail}, f.extra...)}}
	} else {
		assert.Equal(f.t, "Amazon Relational Database Service", input["Service"])
		response = map[string]any{"Recommendations": []any{map[string]any{"RecommendationDetails": append([]any{f.detail}, f.extra...)}}}
	}
	body, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
}

// The CE JSON is constructed; it is not a recorded account or billing response.
func currencySDKRecommendation(t *testing.T, sp bool, payment, term string, code, upfront, recurring *string) ([]common.Recommendation, error) {
	t.Helper()
	detail := map[string]any{"AccountId": "fixture-account", "EstimatedMonthlySavingsAmount": "20",
		"CurrentAverageHourlyOnDemandSpend": "4", "EstimatedMonthlyOnDemandCost": "100"}
	if code != nil {
		detail["CurrencyCode"] = *code
	}
	if upfront != nil {
		detail["UpfrontCost"] = *upfront
	}
	service, recurringField := common.ServiceRDS, "RecurringStandardMonthlyCost"
	if sp {
		service, recurringField = common.ServiceSavingsPlansCompute, "HourlyCommitmentToPurchase"
	} else {
		detail["RecommendedNumberOfInstancesToPurchase"] = "2"
		detail["InstanceDetails"] = map[string]any{"RDSInstanceDetails": map[string]any{
			"InstanceType": "db.m5.large", "DatabaseEngine": "MySQL", "Region": "us-east-1"}}
	}
	if recurring != nil {
		detail[recurringField] = *recurring
	}
	fixture := &currencySDKFixture{t: t, detail: detail, sp: sp, payment: payment, term: term}
	client := recommendations.NewClient(&aws.Config{Region: "us-east-1", HTTPClient: fixture, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		})})
	got, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{
		Service: service, Term: term, PaymentOption: payment, LookbackPeriod: "7d"})
	wantCalls := 1
	if sp && payment == "unknown" {
		wantCalls = 0
	}
	assert.Equal(t, wantCalls, fixture.calls)
	return got, err
}

func TestCommitmentCurrencySDKRequiredLegs(t *testing.T) {
	t.Parallel()
	for _, sp := range []bool{false, true} {
		for _, tc := range []struct {
			name, payment            string
			code, upfront, recurring *string
			want                     string
		}{
			{"partial-USD", "partial-upfront", aws.String("USD"), aws.String("120"), aws.String("10"), "USD"},
			{"currency-absent", "partial-upfront", nil, aws.String("120"), aws.String("10"), ""},
			{"currency-empty", "partial-upfront", aws.String(""), aws.String("120"), aws.String("10"), ""},
			{"currency-blank", "partial-upfront", aws.String(" \t"), aws.String("120"), aws.String("10"), ""},
			{"partial-missing-upfront", "partial-upfront", aws.String("USD"), nil, aws.String("10"), ""},
			{"partial-missing-recurring", "partial-upfront", aws.String("USD"), aws.String("120"), nil, ""},
			{"partial-explicit-zero", "partial-upfront", aws.String("USD"), aws.String("0"), aws.String("0"), "USD"},
			{"all-missing-upfront", "all-upfront", aws.String("USD"), nil, nil, ""},
			{"all-contract-zero", "all-upfront", aws.String("USD"), aws.String("120"), nil, "USD"},
			{"all-explicit-zero", "all-upfront", aws.String("USD"), aws.String("120"), aws.String("0"), "USD"},
			{"no-contract-zero", "no-upfront", aws.String("USD"), nil, aws.String("10"), "USD"},
			{"no-explicit-zero", "no-upfront", aws.String("USD"), aws.String("0"), aws.String("0"), "USD"},
			{"no-missing-recurring", "no-upfront", aws.String("USD"), nil, nil, ""},
			{"no-contradictory-upfront", "no-upfront", aws.String("USD"), aws.String("120"), aws.String("10"), ""},
			{"unknown-payment", "unknown", aws.String("USD"), aws.String("120"), aws.String("10"), ""},
		} {
			t.Run(fmt.Sprintf("sp=%t/%s", sp, tc.name), func(t *testing.T) {
				got, err := currencySDKRecommendation(t, sp, tc.payment, "1yr", tc.code, tc.upfront, tc.recurring)
				if sp && tc.payment == "unknown" {
					require.Error(t, err)
					assert.Empty(t, got)
					return
				}
				require.NoError(t, err)
				require.Len(t, got, 1)
				assert.Equal(t, tc.want, got[0].CommitmentCurrency)
				assertCurrencyJSON(t, got[0], sp)
				if tc.recurring == nil {
					assert.Nil(t, got[0].RecurringMonthlyCost)
				}
			})
		}
	}
}

func TestCommitmentCurrencySDKContradictions(t *testing.T) {
	t.Parallel()
	got, err := currencySDKRecommendation(t, false, "all-upfront", "1yr", aws.String("USD"), aws.String("120"), aws.String("10"))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].CommitmentCurrency)
	assert.Equal(t, 120.0, got[0].CommitmentCost)
	require.NotNil(t, got[0].RecurringMonthlyCost)
	assert.Equal(t, 10.0, *got[0].RecurringMonthlyCost)
	got, err = currencySDKRecommendation(t, true, "no-upfront", "1yr", aws.String("USD"), nil, aws.String("1e308"))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].CommitmentCurrency)
	require.NotNil(t, got[0].RecurringMonthlyCost)
	assert.True(t, math.IsInf(*got[0].RecurringMonthlyCost, 1))
}

func TestCommitmentCurrencySDKMalformedAmounts(t *testing.T) {
	t.Parallel()
	for _, sp := range []bool{false, true} {
		for _, value := range []string{"", "bad", "-1", "NaN", "+Inf"} {
			for _, upfront := range []bool{false, true} {
				t.Run(fmt.Sprintf("sp=%t/upfront=%t/%q", sp, upfront, value), func(t *testing.T) {
					up, recurring := aws.String("120"), aws.String("10")
					if upfront {
						up = aws.String(value)
					} else {
						recurring = aws.String(value)
					}
					got, err := currencySDKRecommendation(t, sp, "partial-upfront", "1yr", aws.String("USD"), up, recurring)
					require.Error(t, err)
					assert.Empty(t, got)
				})
			}
		}
	}
}

func TestCommitmentCurrencySDKAmountsScaleAndJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, payment, term, upfront, recurring string
		sp                                      bool
		wantUpfront, wantMonthly                float64
	}{
		{"RI-partial", "partial-upfront", "1yr", "120", "10", false, 120, 10},
		{"SP-partial-1yr", "partial-upfront", "1yr", "4380", "1", true, 4380, 365},
		{"SP-partial-3yr", "partial-upfront", "3yr", "13140", "1", true, 13140, 365},
		{"SP-no-upfront", "no-upfront", "1yr", "0", "1", true, 0, 730},
		{"SP-all-upfront", "all-upfront", "1yr", "8760", "1", true, 8760, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := currencySDKRecommendation(t, tc.sp, tc.payment, tc.term, aws.String("USD"), aws.String(tc.upfront), aws.String(tc.recurring))
			require.NoError(t, err)
			require.Len(t, got, 1)
			rec := got[0]
			require.Equal(t, "USD", rec.CommitmentCurrency)
			assert.Equal(t, tc.wantUpfront, rec.CommitmentCost)
			require.NotNil(t, rec.RecurringMonthlyCost)
			assert.Equal(t, tc.wantMonthly, *rec.RecurringMonthlyCost)
			scaled := common.ScaleRecommendationCosts(rec, 0.5)
			assert.Equal(t, rec.CommitmentCost/2, scaled.CommitmentCost)
			assert.Equal(t, tc.wantMonthly/2, *scaled.RecurringMonthlyCost)
			assert.NotSame(t, rec.RecurringMonthlyCost, scaled.RecurringMonthlyCost)
			assert.Equal(t, 20.0, rec.EstimatedSavings)
			assert.Equal(t, 10.0, scaled.EstimatedSavings)
			if tc.sp {
				assert.Equal(t, 1, rec.Count)
				assert.Equal(t, 2920.0, rec.OnDemandCost)
			} else {
				assert.Equal(t, 100.0, rec.OnDemandCost)
			}
			if !tc.sp {
				assert.Equal(t, 2, rec.Count)
				resized, err := common.ResizeCount(rec, 1)
				require.NoError(t, err)
				assert.Equal(t, 1, resized.Count)
				assert.Equal(t, 60.0, resized.CommitmentCost)
				assert.Equal(t, 5.0, *resized.RecurringMonthlyCost)
				assert.Equal(t, "USD", resized.CommitmentCurrency)
			}
			assert.Equal(t, "USD", scaled.CommitmentCurrency)
			assert.Equal(t, tc.wantMonthly, *rec.RecurringMonthlyCost)
			if tc.sp {
				original := rec.Details.(*common.SavingsPlanDetails)
				assert.Equal(t, 1.0, original.HourlyCommitment)
				assert.Equal(t, 0.5, scaled.Details.(*common.SavingsPlanDetails).HourlyCommitment)
				assert.Empty(t, original.Currency)
			}
			assertCurrencyJSON(t, scaled, tc.sp)
		})
	}
}

func assertCurrencyJSON(t *testing.T, rec common.Recommendation, sp bool) {
	t.Helper()
	payload, err := json.Marshal(rec)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	if rec.CommitmentCurrency == "" {
		assert.NotContains(t, fields, "commitment_currency")
	} else {
		assert.Equal(t, rec.CommitmentCurrency, fields["commitment_currency"])
	}
	decoded := common.Recommendation{Details: &common.DatabaseDetails{}}
	if sp {
		decoded.Details = &common.SavingsPlanDetails{}
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Equal(t, rec.CommitmentCurrency, decoded.CommitmentCurrency)
	assert.Equal(t, rec.CommitmentCost, decoded.CommitmentCost)
	assert.Equal(t, rec.RecurringMonthlyCost, decoded.RecurringMonthlyCost)
}
