package exchange

// Regression tests for #81: ExpectedAccount was ignored on the
// dependency-injected exchange path (Client.GetQuote / Client.Execute, as
// constructed by NewExchangeClientFromAPI). A caller that set
// ExpectedAccount on that path believed it had a cross-account guard on an
// irreversible purchase and had none: getQuoteWithAPI/executeWithAPI never
// called assertAccount, and executeWithAPI even copied the field into the
// nested quote request where it was equally ignored.

import (
	"context"
	"math/big"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// fakeSTS is a minimal stsIdentityAPI stub returning a fixed account (or an
// error) so tests can drive assertAccount's comparison deterministically.
type fakeSTS struct {
	account string
	err     error
}

func (f *fakeSTS) GetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Account: sdkaws.String(f.account)}, nil
}

// TestClient_GetQuote_ExpectedAccountWithNoIdentityFailsClosed is the core
// regression test: pre-fix, a Client built via NewExchangeClientFromAPI (no
// identity resolver) silently ignored ExpectedAccount and proceeded to call
// the EC2 API. Post-fix, this must fail loud instead.
func TestClient_GetQuote_ExpectedAccountWithNoIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	f := &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("10.00")},
		quoteErrors:  []error{nil},
	}
	c := NewExchangeClientFromAPI(f)

	_, err := c.GetQuote(context.Background(), ExchangeQuoteRequest{
		ExpectedAccount:  "111111111111",
		ReservedIDs:      []string{"ri-1"},
		TargetOfferingID: "off-A",
		TargetCount:      1,
	})

	if err == nil {
		t.Fatal("expected an error when ExpectedAccount is set but no identity resolver is configured, got nil")
	}
	if !strings.Contains(err.Error(), "111111111111") {
		t.Errorf("error should name the unverifiable expected account; got: %v", err)
	}
	if f.quoteCall != 0 {
		t.Errorf("GetReservedInstancesExchangeQuote must not be called when the account guard cannot be verified; called %d time(s)", f.quoteCall)
	}
}

// TestClient_Execute_ExpectedAccountWithNoIdentityFailsClosed is the same
// regression on the Execute path, which is the irreversible purchase call
// the issue is specifically about.
func TestClient_Execute_ExpectedAccountWithNoIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	cap := new(big.Rat).SetInt64(100)
	f := &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("10.00")},
		quoteErrors:  []error{nil},
		acceptOutput: &ec2.AcceptReservedInstancesExchangeQuoteOutput{ExchangeId: sdkaws.String("should-not-be-called")},
	}
	c := NewExchangeClientFromAPI(f)

	_, _, err := c.Execute(context.Background(), ExchangeExecuteRequest{
		ExpectedAccount:  "111111111111",
		ReservedIDs:      []string{"ri-1"},
		TargetOfferingID: "off-A",
		TargetCount:      1,
		MaxPaymentDueUSD: cap,
	})

	if err == nil {
		t.Fatal("expected an error when ExpectedAccount is set but no identity resolver is configured, got nil")
	}
	if f.acceptInput != nil {
		t.Fatalf("Accept was called despite the account guard being unverifiable; accept input: %+v", f.acceptInput)
	}
}

// TestClient_GetQuote_ExpectedAccountMismatchViaInjectedIdentity verifies
// that once an identity resolver is set, a mismatched account is rejected
// -- the guard actually works end to end on the DI path.
func TestClient_GetQuote_ExpectedAccountMismatchViaInjectedIdentity(t *testing.T) {
	t.Parallel()

	f := &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("10.00")},
		quoteErrors:  []error{nil},
	}
	c := NewExchangeClientFromAPI(f)
	c.identity = &fakeSTS{account: "222222222222"}

	_, err := c.GetQuote(context.Background(), ExchangeQuoteRequest{
		ExpectedAccount:  "111111111111",
		ReservedIDs:      []string{"ri-1"},
		TargetOfferingID: "off-A",
		TargetCount:      1,
	})

	if err == nil {
		t.Fatal("expected an error on account mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "111111111111") || !strings.Contains(err.Error(), "222222222222") {
		t.Errorf("error should name both the expected and actual account; got: %v", err)
	}
	if f.quoteCall != 0 {
		t.Errorf("GetReservedInstancesExchangeQuote must not be called on account mismatch; called %d time(s)", f.quoteCall)
	}
}

// TestClient_GetQuote_ExpectedAccountMatchViaInjectedIdentity is the happy
// path: a matching injected identity lets the call through.
func TestClient_GetQuote_ExpectedAccountMatchViaInjectedIdentity(t *testing.T) {
	t.Parallel()

	f := &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("10.00")},
		quoteErrors:  []error{nil},
	}
	c := NewExchangeClientFromAPI(f)
	c.identity = &fakeSTS{account: "111111111111"}

	_, err := c.GetQuote(context.Background(), ExchangeQuoteRequest{
		ExpectedAccount:  "111111111111",
		ReservedIDs:      []string{"ri-1"},
		TargetOfferingID: "off-A",
		TargetCount:      1,
	})

	if err != nil {
		t.Fatalf("expected no error on matching account, got: %v", err)
	}
	if f.quoteCall != 1 {
		t.Errorf("expected exactly one quote call, got %d", f.quoteCall)
	}
}

// TestClient_GetQuote_NoExpectedAccountSkipsGuard verifies the guard stays
// opt-in: a request that never sets ExpectedAccount proceeds without an
// identity resolver, matching the pre-fix and post-fix behavior for the
// common case where no caller wants account verification.
func TestClient_GetQuote_NoExpectedAccountSkipsGuard(t *testing.T) {
	t.Parallel()

	f := &sequentialFakeEC2{
		quoteOutputs: []*ec2.GetReservedInstancesExchangeQuoteOutput{seqQuoteOut("10.00")},
		quoteErrors:  []error{nil},
	}
	c := NewExchangeClientFromAPI(f)

	_, err := c.GetQuote(context.Background(), ExchangeQuoteRequest{
		ReservedIDs:      []string{"ri-1"},
		TargetOfferingID: "off-A",
		TargetCount:      1,
	})

	if err != nil {
		t.Fatalf("expected no error when ExpectedAccount is unset, got: %v", err)
	}
}
