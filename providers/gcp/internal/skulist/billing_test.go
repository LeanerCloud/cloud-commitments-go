package skulist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

type stubBilling struct{}

func (stubBilling) ListSKUs(string) (*cloudbilling.ListSkusResponse, error) { return nil, nil }

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("existing service is returned as is", func(t *testing.T) {
		t.Parallel()
		existing := stubBilling{}
		got, err := Resolve(context.Background(), existing, "cloudsql")
		require.NoError(t, err)
		assert.Equal(t, existing, got)
	})

	t.Run("lists SKUs through the API", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"skus":[{"skuId":"A"}]}`))
		}))
		t.Cleanup(srv.Close)
		svc, err := Resolve(context.Background(), nil, "cloudsql", option.WithEndpoint(srv.URL), option.WithoutAuthentication())
		require.NoError(t, err)
		resp, err := svc.ListSKUs("services/X")
		require.NoError(t, err)
		require.Len(t, resp.Skus, 1)
		assert.Equal(t, "A", resp.Skus[0].SkuId)
	})

	t.Run("list error names the owning service and the billing service", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		svc, err := Resolve(context.Background(), nil, "memorystore", option.WithEndpoint(srv.URL), option.WithoutAuthentication())
		require.NoError(t, err)
		_, err = svc.ListSKUs("services/X")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "memorystore: list SKUs for services/X")
	})
}
