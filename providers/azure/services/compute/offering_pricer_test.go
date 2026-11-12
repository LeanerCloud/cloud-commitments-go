package compute

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)
	for i := 0; i < 2; i++ {
		resp := mocks.CreateMockHTTPResponse(http.StatusOK, mocks.CreateSampleVMPricingResponse())
		t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
		mockHTTP.On("Do", mock.Anything).Return(resp, nil).Once()
	}
	rec := common.Recommendation{ResourceType: "Standard_D2s_v3", Term: "1yr", PaymentOption: "upfront"}

	var sc provider.ServiceClient = client
	pricer, ok := sc.(provider.OfferingPricer)
	require.True(t, ok, "client must implement provider.OfferingPricer")

	got, err := pricer.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	want, err := client.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got)
	assert.Equal(t, "Standard_D2s_v3", got.ResourceType)
	assert.Equal(t, "USD", got.Currency)
	assert.Positive(t, got.UpfrontCost)
}
