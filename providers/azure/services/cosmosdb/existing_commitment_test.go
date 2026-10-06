package cosmosdb

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Issue #211: PurchaseResult.ExistingCommitment must be true only when the
// shared guard adopted an order already tagged with the idempotency token.

type existingCommitmentCred struct{}

func (existingCommitmentCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "tok", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// existingCommitmentHTTP is a stateless ARM stub: the reservation-orders list
// returns listBody, calculatePrice mints "minted-order", and the purchase POST
// answers purchaseStatus. posts counts calculatePrice and purchase calls.
type existingCommitmentHTTP struct {
	listBody       string
	purchaseStatus int
	posts          int
}

func (h *existingCommitmentHTTP) Do(req *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
	path := req.URL.Path
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(path, "/reservationOrders"):
		return respond(http.StatusOK, h.listBody)
	case req.Method == http.MethodGet:
		return respond(http.StatusOK, `{"registrationState":"Registered"}`)
	case strings.HasSuffix(path, "/calculatePrice"):
		h.posts++
		return respond(http.StatusOK, `{"properties":{"reservationOrderId":"minted-order"}}`)
	case strings.HasSuffix(path, "/purchase"):
		h.posts++
		return respond(h.purchaseStatus, `{}`)
	}
	return respond(http.StatusNotFound, `{}`)
}

func existingCommitmentRun(t *testing.T, h *existingCommitmentHTTP) (common.PurchaseResult, error) {
	t.Helper()
	c := NewClientWithHTTP(existingCommitmentCred{}, "sub", "eastus", h)
	rec := common.Recommendation{
		ResourceType:   "EnableCassandra",
		Term:           "1yr",
		Count:          100,
		CommitmentCost: 1000.0,
		PaymentOption:  "no-upfront",
	}
	return c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{Source: common.PurchaseSourceCLI, IdempotencyToken: "tok-211"})
}

func TestPurchaseCommitment_ExistingCommitment_FreshPurchaseNotFlagged(t *testing.T) {
	h := &existingCommitmentHTTP{listBody: `{"value":[]}`, purchaseStatus: http.StatusOK}
	result, err := existingCommitmentRun(t, h)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	assert.Equal(t, "minted-order", result.CommitmentID)
	assert.Equal(t, 2, h.posts)
}

func TestPurchaseCommitment_ExistingCommitment_FailedPurchaseNotFlagged(t *testing.T) {
	h := &existingCommitmentHTTP{listBody: `{"value":[]}`, purchaseStatus: http.StatusInternalServerError}
	result, err := existingCommitmentRun(t, h)
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
}

func TestPurchaseCommitment_ExistingCommitment_RedriveAdoptsAndFlags(t *testing.T) {
	h := &existingCommitmentHTTP{
		listBody:       `{"value":[{"name":"adopted-order","properties":{"provisioningState":"Succeeded"},"tags":{"cudly-idempotency-token":"tok-211"}}]}`,
		purchaseStatus: http.StatusOK,
	}
	result, err := existingCommitmentRun(t, h)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, "adopted-order", result.CommitmentID)
	assert.Zero(t, h.posts, "an adopted order must not trigger a second purchase")
}

// A terminal-failed order carrying the token is not adopted, so the purchase
// proceeds and is not flagged.
func TestPurchaseCommitment_ExistingCommitment_FailedOrderNotAdopted(t *testing.T) {
	h := &existingCommitmentHTTP{
		listBody:       `{"value":[{"name":"dead-order","properties":{"provisioningState":"Failed"},"tags":{"cudly-idempotency-token":"tok-211"}}]}`,
		purchaseStatus: http.StatusOK,
	}
	result, err := existingCommitmentRun(t, h)
	require.NoError(t, err)
	assert.False(t, result.ExistingCommitment)
	assert.Equal(t, "minted-order", result.CommitmentID)
}
