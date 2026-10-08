package exchange

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type guardedFreshExchangeClient struct {
	*mockExchangeClient
	accepted int
}

func (c *guardedFreshExchangeClient) Execute(_ context.Context, req ExchangeExecuteRequest) (string, *ExchangeQuoteSummary, error) {
	c.executeCalls++
	c.executeRequests = append(c.executeRequests, req)
	fresh := c.executeQuoteResult
	if fresh.PaymentDueUSD.Cmp(req.MaxPaymentDueUSD) > 0 {
		return "", fresh, fmt.Errorf("fresh quote exceeds reserved ceiling")
	}
	c.accepted++
	return c.executeResult, fresh, nil
}

func TestRunAutoExchange_FractionalCapNeverRoundsUpBeforeExecute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		dailyCap float64
		perCap   float64
	}{
		{name: "daily cap", dailyCap: 1000.0000009, perCap: 2000},
		{name: "per-exchange cap", dailyCap: 2000, perCap: 1000.0000009},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			initial, err := ParseDecimalRat("900")
			require.NoError(t, err)
			fresh, err := ParseDecimalRat("1000.000001")
			require.NoError(t, err)
			store := &mockExchangeStore{dailySpend: "0"}
			client := &guardedFreshExchangeClient{mockExchangeClient: &mockExchangeClient{
				quoteResult:        &ExchangeQuoteSummary{IsValidExchange: true, CurrencyCode: "USD", PaymentDueUSD: initial},
				executeQuoteResult: &ExchangeQuoteSummary{IsValidExchange: true, CurrencyCode: "USD", PaymentDueUSD: fresh, PaymentDueUSDStr: "1000.000001"},
				executeResult:      "must-not-accept",
			}}
			params := defaultParams(store, client)
			params.Config.Mode = "auto"
			params.Config.MaxPaymentDailyUSD = tc.dailyCap
			params.Config.MaxPaymentPerExchangeUSD = tc.perCap

			result, err := RunAutoExchange(context.Background(), params)

			require.NoError(t, err)
			assert.Zero(t, client.accepted)
			assert.Empty(t, result.Completed)
			require.Len(t, result.Failed, 1)
			require.Len(t, client.executeRequests, 1)
			assert.Equal(t, "1000.000000", client.executeRequests[0].MaxPaymentDueUSD.FloatString(6))
		})
	}
}

func TestRunAutoExchange_NegativeSubCentCapCannotBecomeZero(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0"}
	client := &mockExchangeClient{quoteResult: defaultQuote(), executeResult: "must-not-execute"}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"
	params.Config.MaxPaymentDailyUSD = -0.0000004

	result, err := RunAutoExchange(context.Background(), params)

	require.NoError(t, err)
	assert.Zero(t, client.executeCalls)
	assert.Empty(t, result.Completed)
	require.Len(t, result.Failed, 1)
	assert.Empty(t, store.savedRecords)
}

func autoRecommendation() ReshapeRecommendation {
	return ReshapeRecommendation{
		SourceRIID: "ri-001", SourceInstanceType: "m5.xlarge",
		TargetInstanceType: "m5.large", SourceCount: 1, TargetCount: 2,
		UtilizationPercent: 50,
	}
}

func TestProcessAutoExchange_ReservationFailurePreventsExecution(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0", reserveErr: errors.New("database unavailable")}
	client := &mockExchangeClient{executeResult: "must-not-execute"}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"

	outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "25.000000", big.NewRat(100, 1))

	assert.Contains(t, outcome.Error, "database unavailable")
	assert.False(t, halt)
	assert.Zero(t, client.executeCalls)
	assert.Empty(t, store.savedRecords)
}

func TestProcessAutoExchange_ExecutionFailureMarksReservedRecord(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0"}
	client := &mockExchangeClient{executeErr: errors.New("AWS rejected exchange")}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"

	outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "25.000000", big.NewRat(100, 1))

	assert.Contains(t, outcome.Error, "AWS rejected exchange")
	assert.False(t, halt)
	assert.Equal(t, 1, store.failCalls)
	require.Len(t, store.savedRecords, 1)
	assert.Equal(t, outcome.RecordID, store.savedRecords[0].ID)
	assert.Equal(t, "failed", store.savedRecords[0].Status)
}

func TestProcessAutoExchange_FailedTransitionRetainsReservation(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0", failErr: errors.New("database unavailable")}
	client := &mockExchangeClient{executeErr: errors.New("AWS rejected exchange")}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"

	outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "25.000000", big.NewRat(100, 1))

	assert.Contains(t, outcome.Error, "AWS rejected exchange")
	assert.True(t, halt)
	require.Len(t, store.savedRecords, 1)
	assert.Equal(t, "processing", store.savedRecords[0].Status)
	assert.Equal(t, "100.000000", store.savedRecords[0].PaymentDue)
}

func TestProcessAutoExchange_CompletionRetriesSameRecord(t *testing.T) {
	t.Parallel()
	var settledIDs []string
	store := &mockExchangeStore{dailySpend: "0", completeErrFor: func(id, _, _ string, attempt int) error {
		settledIDs = append(settledIDs, id)
		if attempt < 3 {
			return errors.New("temporary database failure")
		}
		return nil
	}}
	client := &mockExchangeClient{executeResult: "exchange-123"}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"

	outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "25.000000", big.NewRat(100, 1))

	assert.Empty(t, outcome.Error)
	assert.False(t, halt)
	assert.Equal(t, 1, client.executeCalls)
	assert.Equal(t, 3, store.completeCalls)
	assert.Equal(t, []string{outcome.RecordID, outcome.RecordID, outcome.RecordID}, settledIDs)
	require.Len(t, store.savedRecords, 1)
	assert.Equal(t, outcome.RecordID, store.savedRecords[0].ID)
	assert.Equal(t, "completed", store.savedRecords[0].Status)
	assert.Equal(t, "25.000000", store.savedRecords[0].PaymentDue)
}

type brokenReservationStore struct {
	*mockExchangeStore
	missingID       bool
	ceilingOverride string
	reserveCalls    int
}

func (s *brokenReservationStore) ReserveRIExchange(ctx context.Context, record *ExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	s.reserveCalls++
	ceiling, err := s.mockExchangeStore.ReserveRIExchange(ctx, record, dailyCapUSD, perExchangeCapUSD)
	if err != nil {
		return "", err
	}
	if s.missingID {
		record.ID = ""
	}
	if s.ceilingOverride != "" {
		return s.ceilingOverride, nil
	}
	return ceiling, nil
}

func TestProcessAutoExchange_InvalidReservationResponseStopsRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		missingID       bool
		ceilingOverride string
		status          string
	}{
		{name: "missing ID", missingID: true, status: "processing"},
		{name: "invalid ceiling", ceilingOverride: "not-a-number", status: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &brokenReservationStore{mockExchangeStore: &mockExchangeStore{dailySpend: "0"}, missingID: tc.missingID, ceilingOverride: tc.ceilingOverride}
			client := &mockExchangeClient{executeResult: "must-not-execute"}
			params := defaultParams(store, client)
			params.Config.Mode = "auto"

			outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "25.000000", big.NewRat(100, 1))

			assert.Contains(t, outcome.Error, "exchange reservation returned")
			assert.True(t, halt)
			assert.Zero(t, client.executeCalls)
			require.Len(t, store.savedRecords, 1)
			assert.Equal(t, tc.status, store.savedRecords[0].Status)
		})
	}
}

func TestRunAutoExchange_InvalidReservationCeilingReleasesKnownHold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		ceiling    string
		cleanupErr error
		status     string
	}{
		{name: "malformed ceiling", ceiling: "not-a-number", status: "failed"},
		{name: "negative ceiling", ceiling: "-1", status: "failed"},
		{name: "malformed ceiling cleanup fails", ceiling: "not-a-number", cleanupErr: errors.New("database unavailable"), status: "processing"},
		{name: "negative ceiling cleanup fails", ceiling: "-1", cleanupErr: errors.New("database unavailable"), status: "processing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &brokenReservationStore{
				mockExchangeStore: &mockExchangeStore{dailySpend: "0", failErr: tc.cleanupErr},
				ceilingOverride:   tc.ceiling,
			}
			client := &mockExchangeClient{quoteResult: defaultQuote(), executeResult: "must-not-execute"}
			params := defaultParams(store, client)
			params.Config.Mode = "auto"
			params.RIs = append(params.RIs, RIInfo{
				ID: "ri-002", InstanceType: "m5.xlarge", InstanceCount: 1,
				OfferingClass: "convertible", NormalizationFactor: 8,
			})
			params.Utilization = append(params.Utilization, UtilizationInfo{RIID: "ri-002", UtilizationPercent: 50})
			params.RIMetadata["ri-002"] = params.RIMetadata["ri-001"]

			result, err := RunAutoExchange(context.Background(), params)

			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Empty(t, result.Completed)
			assert.Contains(t, result.Failed[0].Error, "invalid ceiling")
			assert.Zero(t, client.executeCalls)
			assert.Equal(t, 1, store.reserveCalls)
			assert.Equal(t, 1, store.failCalls)
			require.Len(t, store.savedRecords, 1)
			assert.Equal(t, result.Failed[0].RecordID, store.savedRecords[0].ID)
			assert.Equal(t, tc.status, store.savedRecords[0].Status)
			if tc.cleanupErr != nil {
				assert.Equal(t, "100.000000", store.savedRecords[0].PaymentDue)
			} else {
				assert.Equal(t, result.Failed[0].Error, store.savedRecords[0].Error)
			}
		})
	}
}

func TestProcessAutoExchange_ZeroQuoteUsesReservedCeiling(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0"}
	client := &mockExchangeClient{executeResult: "exchange-zero"}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"
	params.Config.MaxPaymentDailyUSD = 1000

	outcome, halt := processAutoExchange(context.Background(), params, autoRecommendation(), "offering-123", "0.000000", big.NewRat(1000, 1))

	assert.Empty(t, outcome.Error)
	assert.False(t, halt)
	require.Len(t, client.executeRequests, 1)
	assert.Zero(t, client.executeRequests[0].MaxPaymentDueUSD.Cmp(big.NewRat(1000, 1)))
	require.Len(t, store.savedRecords, 1)
	assert.Equal(t, "completed", store.savedRecords[0].Status)
	assert.Equal(t, "0.000000", store.savedRecords[0].PaymentDue)
}
