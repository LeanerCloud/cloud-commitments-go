package purchasecfg

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
)

func respErr(status int, code string) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      &smithy.GenericAPIError{Code: code, Message: "m"},
	}}
}

func TestClassifyPurchaseError(t *testing.T) {
	op := func(err error) error {
		return &smithy.OperationError{ServiceID: "EC2", OperationName: "Purchase", Err: err}
	}
	cases := []struct {
		name    string
		err     error
		unknown bool
	}{
		{"nil", nil, false},
		{"400 invalid parameter", op(respErr(400, "InvalidParameterValue")), false},
		{"403 unauthorized", op(respErr(403, "UnauthorizedOperation")), false},
		{"400 limit exceeded", op(respErr(400, "ReservedInstancesLimitExceeded")), false},
		{"503 throttle code is definite", op(respErr(503, "RequestLimitExceeded")), false},
		{"429 throttling", op(respErr(429, "Throttling")), false},
		{"500 internal error", op(respErr(500, "InternalError")), true},
		{"503 unavailable (not a throttle code)", op(respErr(503, "ServiceUnavailable")), true},
		{"4xx over MaxAttemptsError stays definite", op(&retry.MaxAttemptsError{Attempt: 2, Err: respErr(400, "InvalidParameterValue")}), false},
		{"500 over MaxAttemptsError", op(&retry.MaxAttemptsError{Attempt: 2, Err: respErr(500, "InternalError")}), true},
		{"200 with undeserializable body", op(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 200}},
			Err:      &smithy.DeserializationError{Err: errors.New("unexpected EOF")},
		}}), true},
		{"transport error", op(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), true},
		{"transport error under MaxAttemptsError", op(&retry.MaxAttemptsError{Attempt: 2, Err: &net.OpError{Op: "read", Err: errors.New("EOF")}}), true},
		{"context deadline", op(context.DeadlineExceeded), true},
		{"context canceled", op(context.Canceled), true},
		{"bare error", errors.New("boom"), true},
		{"ResponseError with nil http response", op(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{}, Err: &smithy.GenericAPIError{Code: "Whatever"},
		}}), true},
		{"4xx without APIError", op(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 400}}, Err: errors.New("opaque"),
		}}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPurchaseError(tc.err)
			if tc.err == nil {
				assert.NoError(t, got)
				return
			}
			assert.Equal(t, tc.unknown, errors.Is(got, ErrOutcomeUnknown))
			assert.ErrorIs(t, got, tc.err, "the original error must stay in the chain")
		})
	}
}

func TestClassifyPurchaseError_Idempotent(t *testing.T) {
	once := ClassifyPurchaseError(errors.New("boom"))
	assert.Same(t, once, ClassifyPurchaseError(once))
}
