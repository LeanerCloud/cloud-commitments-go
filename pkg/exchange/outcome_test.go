package exchange

import (
	"context"
	"errors"
	"math/big"
	"net"
	"net/http"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func statusErr(status int, code string) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      &smithy.GenericAPIError{Code: code},
	}}
}

// acceptRecorder applies the per-call options to a real ec2.Options.
type acceptRecorder struct {
	*sequentialFakeEC2
	attempts int
}

func (a *acceptRecorder) AcceptReservedInstancesExchangeQuote(ctx context.Context, in *ec2.AcceptReservedInstancesExchangeQuoteInput, optFns ...func(*ec2.Options)) (*ec2.AcceptReservedInstancesExchangeQuoteOutput, error) {
	var o ec2.Options
	for _, f := range optFns {
		f(&o)
	}
	a.attempts = o.RetryMaxAttempts
	return a.sequentialFakeEC2.AcceptReservedInstancesExchangeQuote(ctx, in, optFns...)
}

func runAccept(t *testing.T, out *ec2.AcceptReservedInstancesExchangeQuoteOutput, acceptErr error) (*acceptRecorder, string, error) {
	t.Helper()
	f := &acceptRecorder{sequentialFakeEC2: &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("40.00"), seqQuoteOut("40.00")},
		quoteErrors:  []error{nil, nil},
		acceptOutput: out,
		acceptErr:    acceptErr,
	}}
	id, _, err := NewExchangeClientFromAPI(f).Execute(context.Background(), ExchangeExecuteRequest{
		ReservedIDs: []string{"ri-1"}, TargetOfferingID: "off-A", TargetCount: 1,
		MaxPaymentDueUSD: new(big.Rat).SetInt64(100),
	})
	return f, id, err
}

func TestExecute_AcceptSendsOnceAndClassifies(t *testing.T) {
	t.Parallel()
	ok := &ec2.AcceptReservedInstancesExchangeQuoteOutput{ExchangeId: sdkaws.String("exch-1")}
	lost := &net.OpError{Op: "read", Err: errors.New("connection reset")}
	for _, tc := range []struct {
		name    string
		out     *ec2.AcceptReservedInstancesExchangeQuoteOutput
		err     error
		unknown bool
		fails   bool
	}{
		{"success", ok, nil, false, false},
		{"transport error", nil, lost, true, true},
		{"5xx", nil, statusErr(500, "InternalError"), true, true},
		{"400 definite", nil, statusErr(400, "InvalidParameterValue"), false, true},
		{"throttle definite", nil, statusErr(503, "Throttling"), false, true},
		{"empty exchange id", &ec2.AcceptReservedInstancesExchangeQuoteOutput{}, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, _, err := runAccept(t, tc.out, tc.err)
			assert.Equal(t, 1, f.attempts, "accept has no client token and must be sent once")
			assert.Equal(t, tc.fails, err != nil)
			assert.Equal(t, tc.unknown, errors.Is(err, common.ErrOutcomeUnknown))
		})
	}
}

func TestClassifyAcceptError(t *testing.T) {
	t.Parallel()
	assert.NoError(t, classifyAcceptError(nil))
	wrapped := classifyAcceptError(context.DeadlineExceeded)
	assert.ErrorIs(t, wrapped, common.ErrOutcomeUnknown)
	assert.ErrorIs(t, wrapped, context.DeadlineExceeded)
	assert.Equal(t, wrapped, classifyAcceptError(wrapped), "must not double wrap")
}

func TestRunAutoExchange_UnknownAcceptLeavesRecordProcessingAndHalts(t *testing.T) {
	t.Parallel()
	store := &mockExchangeStore{dailySpend: "0"}
	client := &mockExchangeClient{quoteResult: defaultQuote(), executeErr: classifyAcceptError(context.DeadlineExceeded)}
	params := defaultParams(store, client)
	params.Config.Mode = "auto"
	params.RIs = append(params.RIs, RIInfo{ID: "ri-002", InstanceType: "m5.xlarge", InstanceCount: 1, OfferingClass: "convertible", NormalizationFactor: 8})
	params.Utilization = append(params.Utilization, UtilizationInfo{RIID: "ri-002", UtilizationPercent: 50.0})
	params.RIMetadata["ri-002"] = params.RIMetadata["ri-001"]

	result, err := RunAutoExchange(context.Background(), params)
	require.NoError(t, err)
	assert.Equal(t, 1, client.executeCalls, "loop must halt after an unknown accept")
	assert.Zero(t, store.failCalls, "an unknown accept must not release the reservation")
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Error, "outcome unknown")
	require.NotEmpty(t, store.savedRecords)
	assert.NotEqual(t, "failed", store.savedRecords[0].Status)
}
