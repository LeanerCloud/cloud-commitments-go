package exchange

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
)

// classifyAcceptError wraps err with common.ErrOutcomeUnknown unless AWS
// definitely rejected the accept before processing it (an API error with HTTP
// 4xx or a throttle code). Accept has no client token, so after a lost
// response the exchange may have happened.
//
// Mirrors providers/aws/internal/purchasecfg/outcome.go ClassifyPurchaseError;
// pkg cannot import it. Keep the two rules in step.
func classifyAcceptError(err error) error {
	if err == nil || errors.Is(err, common.ErrOutcomeUnknown) {
		return err
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if _, throttled := retry.DefaultThrottleErrorCodes[apiErr.ErrorCode()]; throttled {
			return err
		}
		if status := acceptHTTPStatus(err); status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			return err
		}
	}
	return fmt.Errorf("%w: the accept may have been processed and the exchange may exist; reconcile before retrying: %w", common.ErrOutcomeUnknown, err)
}

func acceptHTTPStatus(err error) int {
	var re *awshttp.ResponseError
	if !errors.As(err, &re) || re == nil || re.ResponseError == nil || re.Response == nil || re.Response.Response == nil {
		return 0
	}
	return re.Response.StatusCode
}
