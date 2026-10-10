package purchasecfg

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
)

// ClassifyPurchaseError wraps err with common.ErrOutcomeUnknown unless AWS definitely
// rejected the request before processing it. It errs toward "unknown": a false
// unknown makes a human check, a false definite allows a double-buy.
//
// Definite (returned unchanged): a service error (smithy.APIError) with an
// HTTP 4xx status, or one whose code is a throttle code (AWS throttles before
// processing, and EC2 reports them as HTTP 503).
//
// Unknown (wrapped): everything else - transport errors, timeouts, context
// cancellation, 5xx, a 200 whose body could not be deserialized, and pre-send
// failures (credentials, DNS, endpoint), which are treated conservatively.
func ClassifyPurchaseError(err error) error {
	if err == nil || errors.Is(err, common.ErrOutcomeUnknown) {
		return err
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if _, throttled := retry.DefaultThrottleErrorCodes[apiErr.ErrorCode()]; throttled {
			return err
		}
		if status := httpStatus(err); status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			return err
		}
	}
	return fmt.Errorf("%w: the request may have been sent and the commitment may exist; reconcile before retrying: %w", common.ErrOutcomeUnknown, err)
}

// httpStatus returns the HTTP status carried by err, or 0 when there is none.
// It never dereferences a partially populated response.
func httpStatus(err error) int {
	var re *awshttp.ResponseError
	if !errors.As(err, &re) || re == nil || re.ResponseError == nil || re.Response == nil || re.Response.Response == nil {
		return 0
	}
	return re.Response.StatusCode
}
