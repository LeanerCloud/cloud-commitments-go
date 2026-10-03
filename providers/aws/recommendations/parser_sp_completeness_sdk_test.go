package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type spSDKRequest struct {
	SavingsPlansType     string
	TermInYears          string
	PaymentOption        string
	LookbackPeriodInDays string
	AccountScope         string
	NextPageToken        string
}

type spSDKFixture struct {
	mu       sync.Mutex
	requests []spSDKRequest
	respond  func(spSDKRequest) (map[string]any, error)
	otherErr bool
}

func (f *spSDKFixture) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.URL.Host != "ce.us-east-1.amazonaws.com" {
		return nil, fmt.Errorf("unexpected fixture host: %s", req.URL.Host)
	}
	var body map[string]any
	var err error
	switch req.Header.Get("X-Amz-Target") {
	case "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation":
		var input spSDKRequest
		if err = json.NewDecoder(req.Body).Decode(&input); err != nil {
			return nil, err
		}
		f.requests = append(f.requests, input)
		body, err = f.respond(input)
	case "AWSInsightsIndexService.GetReservationPurchaseRecommendation":
		body = map[string]any{}
		if f.otherErr {
			err = errors.New("fixture API failure")
		}
	default:
		return nil, fmt.Errorf("unexpected fixture operation: %s", req.Header.Get("X-Amz-Target"))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadRequest
		body = map[string]any{"__type": "InvalidParameterValueException", "Message": err.Error()}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(strings.NewReader(string(encoded))), Request: req}, nil
}

func spSDKClient(f *spSDKFixture) *Client {
	return NewClient(&aws.Config{
		Region: "us-east-1", HTTPClient: f, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		}),
	})
}

func spSDKDetail(account string) map[string]any {
	return map[string]any{
		"AccountId": account, "HourlyCommitmentToPurchase": "2", "EstimatedMonthlySavingsAmount": "10",
		"UpfrontCost": "3", "CurrentAverageHourlyOnDemandSpend": "4",
	}
}

func spSDKPage(token string, details ...map[string]any) map[string]any {
	return map[string]any{"NextPageToken": token, "SavingsPlansPurchaseRecommendation": map[string]any{
		"SavingsPlansPurchaseRecommendationDetails": details,
	}}
}

func spSDKParams(service common.ServiceType) *common.RecommendationParams {
	return &common.RecommendationParams{Service: service, Term: "1yr", PaymentOption: "no-upfront", LookbackPeriod: "7d"}
}

func assertSPIncomplete(t *testing.T, err error, details, scopes int) {
	t.Helper()
	var partial *IncompleteRecommendationsError
	require.ErrorAs(t, err, &partial)
	require.Equal(t, details, partial.FailedDetails)
	require.Equal(t, scopes, partial.FailedScopes)
	require.Len(t, partial.Causes, details+scopes)
	for _, cause := range partial.Causes {
		var nested *IncompleteRecommendationsError
		require.False(t, errors.As(cause, &nested))
	}
}

func TestSPCompletenessSDKDetails(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"HourlyCommitmentToPurchase", "EstimatedMonthlySavingsAmount", "UpfrontCost"} {
		for _, kind := range []string{"valid", "empty", "mixed", "all-invalid"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				t.Parallel()
				valid, bad := spSDKDetail("survivor"), spSDKDetail("rejected")
				bad[field] = "not-a-number"
				details := make([]map[string]any, 1, 2)
				details[0] = valid
				switch kind {
				case "empty":
					details = details[:0]
				case "mixed":
					details = append(details, bad)
				case "all-invalid":
					details = []map[string]any{bad}
				}
				fixture := &spSDKFixture{respond: func(spSDKRequest) (map[string]any, error) { return spSDKPage("", details...), nil }}
				recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), spSDKParams(common.ServiceSavingsPlansCompute))
				require.Equal(t, []spSDKRequest{{SavingsPlansType: "COMPUTE_SP", TermInYears: "ONE_YEAR", PaymentOption: "NO_UPFRONT", LookbackPeriodInDays: "SEVEN_DAYS", AccountScope: "LINKED"}}, fixture.requests)
				if kind == "mixed" || kind == "all-invalid" {
					assertSPIncomplete(t, err, 1, 0)
					require.Contains(t, err.Error(), "plan COMPUTE_SP page 0 detail ")
					require.Contains(t, err.Error(), field)
				} else {
					require.NoError(t, err)
				}
				if kind == "empty" || kind == "all-invalid" {
					require.Empty(t, recs)
					return
				}
				require.Len(t, recs, 1)
				require.Equal(t, "survivor", recs[0].Account)
				require.Equal(t, 10.0, recs[0].EstimatedSavings)
				require.Equal(t, 3.0, recs[0].CommitmentCost)
			})
		}
	}
}

func TestSPCompletenessSDKTypes(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"valid", "empty", "invalid", "all-fail", "single-fail"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := &spSDKFixture{respond: func(in spSDKRequest) (map[string]any, error) {
				if in.SavingsPlansType == "DATABASE_SP" || kind == "all-fail" {
					return nil, errors.New("fixture API failure")
				}
				if kind == "empty" {
					return spSDKPage(""), nil
				}
				detail := spSDKDetail(in.SavingsPlansType)
				if kind == "invalid" {
					detail["HourlyCommitmentToPurchase"] = "bad"
				}
				return spSDKPage("", detail), nil
			}}
			params := spSDKParams(common.ServiceSavingsPlansAll)
			if kind == "single-fail" {
				params.Service = common.ServiceSavingsPlansDatabase
			}
			recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), params)
			calls := 4
			if kind == "single-fail" {
				calls = 1
			}
			require.Len(t, fixture.requests, calls)
			if kind == "all-fail" || kind == "single-fail" {
				require.ErrorContains(t, err, "fixture API failure")
				var partial *IncompleteRecommendationsError
				require.False(t, errors.As(err, &partial))
				require.Empty(t, recs)
				return
			}
			details := 0
			if kind == "invalid" {
				details = 3
			}
			assertSPIncomplete(t, err, details, 1)
			require.Contains(t, err.Error(), "DATABASE_SP")
			require.Len(t, fixture.requests, 4)
			if kind == "valid" {
				require.Len(t, recs, 3)
			} else {
				require.Empty(t, recs)
			}
		})
	}
}

func TestSPCompletenessSDKPages(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"valid", "empty", "invalid", "absent"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", first, failed), func(t *testing.T) {
				t.Parallel()
				fixture := &spSDKFixture{respond: func(in spSDKRequest) (map[string]any, error) {
					if in.NextPageToken == "next" {
						if failed {
							return nil, errors.New("late page failure")
						}
						return spSDKPage("", spSDKDetail("second")), nil
					}
					if first == "absent" {
						return map[string]any{"NextPageToken": "next"}, nil
					}
					if first == "empty" {
						return spSDKPage("next"), nil
					}
					detail := spSDKDetail("first")
					if first == "invalid" {
						detail["HourlyCommitmentToPurchase"] = "bad"
					}
					return spSDKPage("next", detail), nil
				}}
				recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), spSDKParams(common.ServiceSavingsPlansCompute))
				details, scopes := 0, 0
				var accounts []string
				if first == "valid" {
					accounts = append(accounts, "first")
				}
				if first == "invalid" {
					details = 1
				}
				if failed {
					scopes = 1
				} else {
					accounts = append(accounts, "second")
				}
				if details+scopes > 0 {
					assertSPIncomplete(t, err, details, scopes)
				} else {
					require.NoError(t, err)
				}
				require.Len(t, recs, len(accounts))
				for i, account := range accounts {
					require.Equal(t, account, recs[i].Account)
				}
				require.Len(t, fixture.requests, 2)
				require.Empty(t, fixture.requests[0].NextPageToken)
				require.Equal(t, "next", fixture.requests[1].NextPageToken)
			})
		}
	}
}

func TestSPCompletenessSDKAggregation(t *testing.T) {
	t.Parallel()
	for _, allServices := range []bool{false, true} {
		for _, kind := range []string{"mixed", "failed-combo", "failed-service", "all-failed"} {
			t.Run(fmt.Sprintf("all=%t/%s", allServices, kind), func(t *testing.T) {
				t.Parallel()
				fixture := &spSDKFixture{otherErr: kind == "all-failed", respond: func(in spSDKRequest) (map[string]any, error) {
					if kind == "failed-service" || kind == "all-failed" ||
						(kind == "failed-combo" && in.TermInYears == "ONE_YEAR" && in.PaymentOption == "ALL_UPFRONT") ||
						(kind == "mixed" && in.SavingsPlansType == "DATABASE_SP") {
						return nil, errors.New("fixture API failure")
					}
					if kind == "mixed" && in.SavingsPlansType == "COMPUTE_SP" {
						bad := spSDKDetail("rejected")
						bad["HourlyCommitmentToPurchase"] = "bad"
						return spSDKPage("", spSDKDetail("survivor"), bad), nil
					}
					return spSDKPage(""), nil
				}}
				client := spSDKClient(fixture)
				var recs []common.Recommendation
				var err error
				if allServices {
					recs, err = client.GetAllRecommendations(context.Background())
				} else {
					recs, err = client.GetRecommendationsForService(context.Background(), common.ServiceSavingsPlansAll)
				}
				expected := make([]spSDKRequest, 0, 24)
				for _, term := range []string{"ONE_YEAR", "THREE_YEARS"} {
					for _, payment := range []string{"ALL_UPFRONT", "PARTIAL_UPFRONT", "NO_UPFRONT"} {
						for _, plan := range []string{"COMPUTE_SP", "EC2_INSTANCE_SP", "SAGEMAKER_SP", "DATABASE_SP"} {
							expected = append(expected, spSDKRequest{SavingsPlansType: plan, TermInYears: term, PaymentOption: payment, LookbackPeriodInDays: "SEVEN_DAYS", AccountScope: "LINKED"})
						}
					}
				}
				require.Equal(t, expected, fixture.requests)
				switch {
				case kind == "mixed":
					assertSPIncomplete(t, err, 6, 6)
					require.Len(t, recs, 6)
					for i, rec := range recs {
						require.Equal(t, []string{"1yr", "3yr"}[i/3], rec.Term)
						require.Equal(t, []string{"all-upfront", "partial-upfront", "no-upfront"}[i%3], rec.PaymentOption)
					}
				case kind == "failed-combo" || (kind == "failed-service" && allServices):
					assertSPIncomplete(t, err, 0, 1)
					require.Empty(t, recs)
				default:
					require.Error(t, err)
					var partial *IncompleteRecommendationsError
					require.False(t, errors.As(err, &partial))
					require.Empty(t, recs)
				}
			})
		}
	}
}

func TestSPCompletenessSDKPageDiagnosticProvenance(t *testing.T) {
	t.Parallel()
	fixture := &spSDKFixture{respond: func(in spSDKRequest) (map[string]any, error) {
		if in.SavingsPlansType != "COMPUTE_SP" {
			return spSDKPage("", spSDKDetail(in.SavingsPlansType)), nil
		}
		if in.NextPageToken == "third" {
			return nil, errors.New("third page failed")
		}
		account, token := "first", "second"
		if in.NextPageToken == "second" {
			account, token = "second", "third"
		}
		bad := spSDKDetail("rejected-" + account)
		bad["HourlyCommitmentToPurchase"] = "bad-" + account
		return spSDKPage(token, spSDKDetail(account), bad), nil
	}}
	recs, err := spSDKClient(fixture).GetRecommendations(context.Background(), spSDKParams(common.ServiceSavingsPlansAll))
	assertSPIncomplete(t, err, 2, 1)
	require.Len(t, recs, 5)
	for i, account := range []string{"first", "second", "EC2_INSTANCE_SP", "SAGEMAKER_SP", "DATABASE_SP"} {
		require.Equal(t, account, recs[i].Account)
	}
	var partial *IncompleteRecommendationsError
	require.ErrorAs(t, err, &partial)
	for page := range 2 {
		require.Contains(t, partial.Causes[page].Error(), fmt.Sprintf("service %s term 1yr payment no-upfront plan COMPUTE_SP page %d detail 1", common.ServiceSavingsPlansAll, page))
	}
	require.Contains(t, partial.Causes[2].Error(), "plan COMPUTE_SP page 2")
	require.Contains(t, partial.Causes[2].Error(), "third page failed")
	tokens := make([]string, 0, len(fixture.requests))
	for _, request := range fixture.requests {
		tokens = append(tokens, request.NextPageToken)
	}
	require.Equal(t, []string{"", "second", "third", "", "", ""}, tokens)
}
