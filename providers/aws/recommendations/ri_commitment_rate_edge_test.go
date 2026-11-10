package recommendations

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/require"
)

func TestGetRICommitmentRate_BadDetailHandling(t *testing.T) {
	t.Parallel()
	good := riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")
	p := riParams("1yr", "no-upfront")

	// A matching detail with an absent field poisons the rate.
	absent := riRateDetail("acct-a", "us-east-1", "700", "", "300", "")
	got, err := riClient(good, absent).GetRICommitmentRate(context.Background(), p)
	if !errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
		t.Fatalf("matching detail with nil utilization: r=%v err=%v, want the sentinel", got, err)
	}

	// A matching detail with an unparseable field is a plain error.
	garbled := riRateDetail("acct-a", "us-east-1", "700", "", "abc", "100")
	got, err = riClient(good, garbled).GetRICommitmentRate(context.Background(), p)
	if err == nil || errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
		t.Fatalf("matching detail with bad savings: r=%v err=%v, want a plain error", got, err)
	}

	// Details outside the account or region are filtered before parsing.
	junk := func(acct, region string) types.ReservationPurchaseRecommendationDetail {
		d := riRateDetail(acct, region, "-1", "", "NaN", "abc")
		return d
	}
	noInstance := junk("acct-a", "us-east-1")
	noInstance.InstanceDetails = nil
	requireRate(t, riClient(good, junk("acct-b", "us-east-1"), junk("acct-a", "EU (Ireland)"), noInstance), p, 0.7)
}

func TestGetRICommitmentRate_Sentinel(t *testing.T) {
	t.Parallel()
	other := riRateDetail("acct-b", "us-east-1", "700", "", "300", "100")
	zeroCovered := riRateDetail("acct-a", "us-east-1", "0", "", "0", "100")
	cases := []struct {
		name    string
		payment string
		client  *Client
	}{
		{"no details", "no-upfront", riClient()},
		{"empty recommendations", "no-upfront", riClientPages()},
		{"no matching detail", "no-upfront", riClient(other)},
		{"nil savings", "no-upfront", riClient(riRateDetail("acct-a", "us-east-1", "700", "", "", "100"))},
		{"nil utilization", "no-upfront", riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", ""))},
		{"nil recurring no-upfront", "no-upfront", riClient(riRateDetail("acct-a", "us-east-1", "", "", "300", "100"))},
		{"nil upfront partial", "partial-upfront", riClient(riRateDetail("acct-a", "us-east-1", "350", "", "300", "100"))},
		{"nil recurring partial", "partial-upfront", riClient(riRateDetail("acct-a", "us-east-1", "", "4200", "300", "100"))},
		{"nil upfront all-upfront", "all-upfront", riClient(riRateDetail("acct-a", "us-east-1", "", "", "300", "100"))},
		{"covered zero", "no-upfront", riClient(zeroCovered)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.client.GetRICommitmentRate(context.Background(), riParams("1yr", tc.payment))
			if !errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
				t.Fatalf("r=%v err=%v, want the sentinel", got, err)
			}
		})
	}
}

func TestGetRICommitmentRate_PlainErrors(t *testing.T) {
	t.Parallel()
	base := riParams("1yr", "no-upfront")
	requirePlain := func(t *testing.T, c *Client, p RICommitmentRateParams) {
		t.Helper()
		got, err := c.GetRICommitmentRate(context.Background(), p)
		if err == nil || errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
			t.Fatalf("r=%v err=%v, want a plain error", got, err)
		}
	}
	for _, v := range []string{"abc", "NaN", "+Inf", "-0.5", ""} {
		for _, field := range []string{"savings", "util", "recurring"} {
			d := riRateDetail("acct-a", "us-east-1", "700", "", "300", "100")
			switch field {
			case "savings":
				d.EstimatedMonthlySavingsAmount = &v
			case "util":
				d.AverageUtilization = &v
			default:
				d.RecurringStandardMonthlyCost = &v
			}
			requirePlain(t, riClient(d), base)
		}
	}
	// Zero utilization gives a non-positive r.
	requirePlain(t, riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "0")), base)
	good := riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "100"))
	for name, mutate := range map[string]func(*RICommitmentRateParams){
		"account":  func(p *RICommitmentRateParams) { p.AccountID = "" },
		"region":   func(p *RICommitmentRateParams) { p.Region = "" },
		"term":     func(p *RICommitmentRateParams) { p.Term = "2yr" },
		"payment":  func(p *RICommitmentRateParams) { p.PaymentOption = "x" },
		"lookback": func(p *RICommitmentRateParams) { p.LookbackPeriod = "9d" },
	} {
		p := base
		mutate(&p)
		t.Run(name, func(t *testing.T) { requirePlain(t, good, p) })
	}
}

func TestGetRICommitmentRate_CanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := riClient(riRateDetail("acct-a", "us-east-1", "700", "", "300", "100"))
	got, err := c.GetRICommitmentRate(ctx, riParams("1yr", "no-upfront"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNoRICommitmentRate) || got != 0 {
		t.Fatalf("r=%v err=%v, want the raw context error", got, err)
	}
}

func riSDKDetail(recurring, savings string) map[string]any {
	return map[string]any{
		"AccountId": "acct-a", "RecurringStandardMonthlyCost": recurring, "UpfrontCost": "0",
		"EstimatedMonthlySavingsAmount": savings, "AverageUtilization": "100",
		"InstanceDetails": map[string]any{"EC2InstanceDetails": map[string]any{"Region": "US East (N. Virginia)"}},
	}
}

func riSDKPage(token string, details ...map[string]any) map[string]any {
	page := map[string]any{"Recommendations": []any{map[string]any{"RecommendationDetails": details}}}
	if token != "" {
		page["NextPageToken"] = token
	}
	return page
}

func TestGetRICommitmentRate_PagesAndRequestShape(t *testing.T) {
	t.Parallel()
	pages := []map[string]any{
		riSDKPage("next", riSDKDetail("700", "300")),
		riSDKPage("", riSDKDetail("300", "200")),
	}
	fixture := &spSDKFixture{riRespond: func(in spSDKRequest) (map[string]any, error) {
		if in.NextPageToken == "" {
			return pages[0], nil
		}
		return pages[1], nil
	}}
	// (700+300)/(1000+500) = 0.6667; page 1 alone would be 0.7.
	requireRate(t, spSDKClient(fixture), riParams("3yr", "partial-upfront"), 1000.0/1500.0)
	require.Len(t, fixture.riRequests, 2)
	for i, req := range fixture.riRequests {
		require.Equal(t, "Amazon Elastic Compute Cloud - Compute", req.Service)
		require.Equal(t, "CONVERTIBLE", req.ServiceSpecification.EC2Specification.OfferingClass)
		require.Equal(t, "LINKED", req.AccountScope)
		require.Equal(t, "THREE_YEARS", req.TermInYears)
		require.Equal(t, "PARTIAL_UPFRONT", req.PaymentOption)
		require.Equal(t, "THIRTY_DAYS", req.LookbackPeriodInDays)
		require.Equal(t, []string{"", "next"}[i], req.NextPageToken)
	}
}

func TestGetRICommitmentRate_EmptyPagesAndNoDetails(t *testing.T) {
	t.Parallel()
	fixture := &spSDKFixture{riRespond: func(in spSDKRequest) (map[string]any, error) {
		if in.NextPageToken == "" {
			return riSDKPage("next"), nil
		}
		return riSDKPage("", riSDKDetail("700", "300")), nil
	}}
	requireRate(t, spSDKClient(fixture), riParams("1yr", "no-upfront"), 0.7)

	empty := &spSDKFixture{riRespond: func(spSDKRequest) (map[string]any, error) { return map[string]any{}, nil }}
	_, err := spSDKClient(empty).GetRICommitmentRate(context.Background(), riParams("1yr", "no-upfront"))
	require.ErrorIs(t, err, ErrNoRICommitmentRate)
}

func TestGetRICommitmentRate_PageCap(t *testing.T) {
	t.Parallel()
	fixture := &spSDKFixture{riRespond: func(spSDKRequest) (map[string]any, error) {
		return riSDKPage("again", riSDKDetail("700", "300")), nil
	}}
	got, err := spSDKClient(fixture).GetRICommitmentRate(context.Background(), riParams("1yr", "no-upfront"))
	require.Zero(t, got)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoRICommitmentRate)
	require.True(t, strings.Contains(err.Error(), "pagination cap reached after "+strconv.Itoa(maxRecommendationPages)+" pages"), err.Error())
	require.Len(t, fixture.riRequests, 20)
}
