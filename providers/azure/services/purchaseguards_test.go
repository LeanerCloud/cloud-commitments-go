package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

const (
	errSource       = "purchase source is required for Azure reservation purchases"
	errCountZero    = "quantity must be greater than zero, got 0"
	errCountNoDigit = "quantity must be greater than zero"
	errResType      = "resource type is required"
	errBadTerm      = "unsupported reservation term: 2yr"
	errBadPayment   = `azure reservations support only upfront or monthly billing; "partial-upfront" is not available (partial-upfront has no azure equivalent)`
	errNoPayment    = "azure reservations support only upfront or monthly billing, and no payment option was supplied; " +
		"set it explicitly (upfront or monthly) rather than relying on a default, because Azure's own " +
		"default is upfront and would charge the whole commitment immediately"
	errAccessToken = "failed to get access token: token boom"
	errLookup      = "idempotency lookup failed before Azure reservation purchase (refusing to purchase to avoid a possible double-buy): "
)

func validRec() common.Recommendation {
	return common.Recommendation{ResourceType: "GP_Gen5_8", Term: "1yr", Count: 2, PaymentOption: "monthly"}
}

func validOpts() common.PurchaseOptions {
	return common.PurchaseOptions{Source: common.PurchaseSourceCLI, IdempotencyToken: goldenToken}
}

type guardCase struct {
	name   string
	rec    func(common.Recommendation) common.Recommendation
	opts   func(common.PurchaseOptions) common.PurchaseOptions
	wantBy func(service string) string
}

func same(msg string) func(string) string { return func(string) string { return msg } }

func guardCases() []guardCase {
	id := func(r common.Recommendation) common.Recommendation { return r }
	idOpts := func(o common.PurchaseOptions) common.PurchaseOptions { return o }
	return []guardCase{
		{"missing source", id, func(o common.PurchaseOptions) common.PurchaseOptions { o.Source = ""; return o }, same(errSource)},
		{"zero count", func(r common.Recommendation) common.Recommendation { r.Count = 0; return r }, idOpts,
			func(s string) string {
				if s == "synapse" {
					return errCountNoDigit
				}
				return errCountZero
			}},
		{"blank resource type", func(r common.Recommendation) common.Recommendation { r.ResourceType = "  "; return r }, idOpts, same(errResType)},
		{"bad term", func(r common.Recommendation) common.Recommendation { r.Term = "2yr"; return r }, idOpts,
			func(s string) string {
				if s == "compute" {
					return "failed to marshal request: " + errBadTerm
				}
				return errBadTerm
			}},
		{"bad payment option", func(r common.Recommendation) common.Recommendation { r.PaymentOption = "partial-upfront"; return r }, idOpts, same(errBadPayment)},
		{"empty payment option", func(r common.Recommendation) common.Recommendation { r.PaymentOption = ""; return r }, idOpts, same(errNoPayment)},
		{"bad term and bad payment option", func(r common.Recommendation) common.Recommendation {
			r.Term, r.PaymentOption = "2yr", "partial-upfront"
			return r
		}, idOpts, func(s string) string {
			if s == "compute" {
				// compute resolves the billing plan before the term.
				return errBadPayment
			}
			return errBadTerm
		}},
	}
}

// Every guard, term and payment error returns before any HTTP or token call,
// even with an idempotency token set (which would otherwise trigger a lookup).
func TestPurchaseGuardErrors(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		for _, c := range guardCases() {
			want := c.wantBy(gc.name)
			if want == "" {
				continue // case does not apply to this service (it has no such guard)
			}
			t.Run(gc.name+"/"+c.name, func(t *testing.T) {
				h := &reservationstest.Capture{}
				cred := &reservationstest.CountingCred{}
				res, err := gc.build(cred, h).PurchaseCommitment(context.Background(), c.rec(validRec()), c.opts(validOpts()))
				require.EqualError(t, err, want)
				require.False(t, res.Success)
				require.Equal(t, err, res.Error)
				require.Zero(t, h.Calls, "no HTTP call: %v", h.Sequence())
				require.Zero(t, cred.Calls, "no GetToken call")
			})
		}
	}
}

// The lookup failure is returned as the shared guard builds it, with no
// further wrapping by the client, and nothing is purchased.
func TestPurchaseLookupFailure(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		t.Run(gc.name, func(t *testing.T) {
			h := &reservationstest.Capture{FailWhen: func(r *reservationstest.Request) error {
				if r.Method == methodGet && r.URL == urlOrders {
					return errors.New("orders boom")
				}
				return nil
			}}
			cred := &reservationstest.CountingCred{}
			res, err := gc.build(cred, h).PurchaseCommitment(context.Background(), validRec(), validOpts())
			require.Error(t, err)
			require.Equal(t, err, res.Error)
			require.False(t, res.Success)
			require.Equal(t, errLookup+"list reservation orders HTTP call: orders boom", err.Error())
			require.Equal(t, append(append([]string{}, gc.preflight...), methodGet+" "+urlOrders), h.Sequence(), "no calculatePrice or purchase after a failed lookup")
		})
	}
}

// A failing credential surfaces as "failed to get access token". Compute
// calls GetToken first for provider registration (a failure there is only
// logged), then again for the purchase.
func TestPurchaseTokenFailure(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		t.Run(gc.name, func(t *testing.T) {
			h := &reservationstest.Capture{}
			cred := &reservationstest.CountingCred{Err: errors.New("token boom")}
			res, err := gc.build(cred, h).PurchaseCommitment(context.Background(), validRec(), validOpts())
			require.EqualError(t, err, errAccessToken)
			require.Equal(t, err, res.Error)
			require.False(t, res.Success)
			wantTokens := 1
			if gc.name == "compute" {
				wantTokens = 2
			}
			require.Equal(t, wantTokens, cred.Calls)
			require.Zero(t, h.Calls, "no HTTP call: %v", h.Sequence())
		})
	}
}

// Monthly billing costs nothing upfront (Cost points at zero); upfront leaves
// Cost nil. A fresh purchase is never flagged as an existing commitment.
func TestPurchaseCostAndExisting(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		for _, c := range []struct {
			payment  string
			wantZero bool
		}{{"monthly", true}, {"upfront", false}} {
			t.Run(gc.name+"/"+c.payment, func(t *testing.T) {
				rec := validRec()
				rec.PaymentOption = c.payment
				res, err := gc.build(&reservationstest.CountingCred{}, &reservationstest.Capture{}).PurchaseCommitment(context.Background(), rec, validOpts())
				require.NoError(t, err)
				require.True(t, res.Success)
				require.False(t, res.ExistingCommitment)
				if c.wantZero {
					require.NotNil(t, res.Cost)
					require.Zero(t, *res.Cost)
				} else {
					require.Nil(t, res.Cost)
				}
			})
		}
	}
}
