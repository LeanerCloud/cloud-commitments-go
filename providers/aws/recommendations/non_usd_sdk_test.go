package recommendations_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
)

// A non-USD detail is skipped, the other details survive, and the skip is
// visible to the caller as an IncompleteRecommendationsError wrapping ErrNotUSD.
func TestNonUSDDetailIsSkippedAndSurfaced(t *testing.T) {
	t.Parallel()
	for _, sp := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			currency *string
			wantSkip bool
		}{
			{"USD kept", aws.String("USD"), false},
			{"lowercase usd kept", aws.String("usd"), false},
			{"EUR skipped", aws.String("EUR"), true},
			{"JPY skipped", aws.String("JPY"), true},
		} {
			name := "ri/" + tc.name
			if sp {
				name = "sp/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				detail := func(account string, code *string) map[string]any {
					d := map[string]any{"AccountId": account, "EstimatedMonthlySavingsAmount": "20",
						"CurrentAverageHourlyOnDemandSpend": "4", "EstimatedMonthlyOnDemandCost": "100",
						"UpfrontCost": "0"}
					if code != nil {
						d["CurrencyCode"] = *code
					}
					if sp {
						d["HourlyCommitmentToPurchase"] = "1"
					} else {
						d["RecurringStandardMonthlyCost"] = "10"
						d["RecommendedNumberOfInstancesToPurchase"] = "2"
						d["InstanceDetails"] = map[string]any{"RDSInstanceDetails": map[string]any{
							"InstanceType": "db.m5.large", "DatabaseEngine": "MySQL", "Region": "us-east-1"}}
					}
					return d
				}
				fixture := &currencySDKFixture{t: t, sp: sp, payment: "no-upfront", term: "1yr",
					detail: detail("bad-account", tc.currency), extra: []any{detail("good-account", aws.String("USD"))}}
				service := common.ServiceRDS
				if sp {
					service = common.ServiceSavingsPlansCompute
				}
				client := recommendations.NewClient(&aws.Config{Region: "us-east-1", HTTPClient: fixture, RetryMaxAttempts: 1,
					Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
						return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
					})})
				got, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{
					Service: service, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d"})
				if !tc.wantSkip {
					require.NoError(t, err)
					assert.Len(t, got, 2)
					return
				}
				require.Len(t, got, 1)
				assert.Equal(t, "good-account", got[0].Account)
				require.ErrorIs(t, err, recommendations.ErrNotUSD)
				var incomplete *recommendations.IncompleteRecommendationsError
				require.ErrorAs(t, err, &incomplete)
				assert.Equal(t, 1, incomplete.FailedDetails)
				assert.Contains(t, err.Error(), "bad-account")
				assert.Contains(t, err.Error(), *tc.currency)
			})
		}
	}
}
