// Package purchasetest is imported only by tests. It holds the shared empty-token PurchaseCommitment cases
// for the services whose purchase API has no idempotency token of its own
// (see go#299). It is test support only.
package purchasetest

import (
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Call runs one PurchaseCommitment against a fake whose Purchase API returns
// apiErr (or an empty success when emptyResp is set, or a normal success when
// both are zero). It returns the RetryMaxAttempts the call asked for (0 when
// no per-operation option was passed).
type Call func(t *testing.T, token string, apiErr error, emptyResp bool) (attempts int, result common.PurchaseResult, err error)

// RespErr wraps inner with an HTTP status; a bare typed fault classifies as unknown.
func RespErr(status int, inner error) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      inner,
	}}
}

// Pick returns the fake output: nil on apiErr, empty if emptyResp, else ok.
func Pick[T any](ok, empty *T, apiErr error, emptyResp bool) *T {
	switch {
	case apiErr != nil:
		return nil
	case emptyResp:
		return empty
	}
	return ok
}

// RunEmptyToken checks the go#299 behavior. alreadyExists is the service's
// typed AlreadyExists fault.
func RunEmptyToken(t *testing.T, call Call, alreadyExists error) {
	t.Helper()
	attempts, result, err := call(t, "", nil, false)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 1, attempts, "empty token must send once")
	attempts, _, err = call(t, "exec-token", nil, false)
	require.NoError(t, err)
	assert.Equal(t, 0, attempts, "token path must keep the client default retries")

	lost := &net.OpError{Op: "read", Err: errors.New("connection reset")}
	for _, tc := range []struct {
		name, token string
		apiErr      error
		emptyResp   bool
		unknown     bool
	}{
		{"transport error", "", lost, false, true},
		{"5xx", "", RespErr(http.StatusInternalServerError, &smithy.GenericAPIError{Code: "InternalFailure"}), false, true},
		{"empty response", "", nil, true, true},
		{"400 AlreadyExists", "", RespErr(http.StatusBadRequest, alreadyExists), false, false},
		{"throttle", "", RespErr(http.StatusServiceUnavailable, &smithy.GenericAPIError{Code: "Throttling"}), false, false},
		{"token: transport error", "exec-token", lost, false, false},
		{"token: empty response", "exec-token", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, result, err := call(t, tc.token, tc.apiErr, tc.emptyResp)
			require.Error(t, err)
			assert.False(t, result.Success)
			assert.Equal(t, tc.unknown, errors.Is(err, common.ErrOutcomeUnknown))
		})
	}
}
