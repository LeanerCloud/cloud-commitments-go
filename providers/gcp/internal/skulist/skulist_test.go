package skulist

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

func newCall(t *testing.T, h http.HandlerFunc) *cloudbilling.ServicesSkusListCall {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	api, err := cloudbilling.NewService(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	require.NoError(t, err)
	return api.Services.Skus.List("services/x")
}

func TestAll_FollowsNextPageToken(t *testing.T) {
	t.Parallel()
	var tokens []string
	call := newCall(t, func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("pageToken")
		tokens = append(tokens, tok)
		w.Header().Set("Content-Type", "application/json")
		switch tok {
		case "":
			fmt.Fprint(w, `{"skus":[{"skuId":"A"}],"nextPageToken":"t2"}`)
		case "t2":
			fmt.Fprint(w, `{"skus":[{"skuId":"B"}],"nextPageToken":"t3"}`)
		default:
			fmt.Fprint(w, `{"skus":[{"skuId":"C"}]}`)
		}
	})
	skus, err := All(context.Background(), call, 3)
	require.NoError(t, err)
	require.Len(t, skus, 3)
	assert.Equal(t, []string{"", "t2", "t3"}, tokens)
}

func TestAll_ExceedingCapErrorsInsteadOfReturningPartialList(t *testing.T) {
	t.Parallel()
	call := newCall(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"skus":[{"skuId":"A"}],"nextPageToken":"more"}`)
	})
	skus, err := All(context.Background(), call, 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded 3 pages")
	assert.Nil(t, skus)
}
