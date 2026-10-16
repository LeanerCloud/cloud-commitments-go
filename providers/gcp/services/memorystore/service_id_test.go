package memorystore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

type serviceIDSpy struct{ got []string }

func (s *serviceIDSpy) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	s.got = append(s.got, serviceID)
	return &cloudbilling.ListSkusResponse{}, nil
}

// Regression for #380: the previous ID returned 404 from the live Billing API.
func TestListSKUs_UsesMemorystoreForRedisServiceID(t *testing.T) {
	client, err := NewClient(context.Background(), "test-project", "us-central1")
	require.NoError(t, err)
	spy := &serviceIDSpy{}

	_, err = client.listSKUs(spy)

	require.NoError(t, err)
	assert.Equal(t, []string{"services/5AF5-2C11-D467"}, spy.got)
}
