// Package exchange provides AWS Convertible Reserved Instance exchange operations.
// It wraps the EC2 GetReservedInstancesExchangeQuote and AcceptReservedInstancesExchangeQuote
// APIs with input validation and spend-cap guardrails.
package exchange

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ExchangeQuoteSummary is a small, stable summary we can log/guard on.
//
//nolint:revive // stutters, but cloud-commitments-platform references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeQuoteSummary struct {
	IsValidExchange         bool
	ValidationFailureReason string
	CurrencyCode            string

	PaymentDueRaw    string   // as returned by AWS (string)
	PaymentDueUSD    *big.Rat `json:"-"`                         // internal use only, not serializable
	PaymentDueUSDStr string   `json:"payment_due_usd,omitempty"` // parsed decimal for JSON consumers

	OutputReservedInstancesExp string // formatted date string (YYYY-MM-DD), empty if not set

	// Rollups (strings in AWS response)
	SourceHourlyPriceRaw      string
	SourceRemainingUpfrontRaw string
	SourceRemainingTotalRaw   string
	TargetHourlyPriceRaw      string
	TargetRemainingUpfrontRaw string
	TargetRemainingTotalRaw   string
}

// ParseDecimalRat parses AWS decimal strings like "123.45" or "-0.018000" into big.Rat.
func ParseDecimalRat(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty decimal string")
	}
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok {
		return nil, fmt.Errorf("invalid decimal: %q", s)
	}
	return r, nil
}

// TargetConfig is a single target offering in an exchange: a Convertible
// RI offering to buy and how many of it. AWS accepts multiple targets
// per exchange (AcceptReservedInstancesExchangeQuote is all-or-nothing
// across the whole TargetConfigurations slice), which lets callers
// redistribute RI value across several shapes in one atomic operation.
type TargetConfig struct {
	OfferingID string
	Count      int32
}

// ExchangeQuoteRequest holds parameters for requesting an exchange quote.
//
//nolint:revive // stutters, but ci_cd_sanity_tests references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeQuoteRequest struct {
	Region          string
	ExpectedAccount string // optional safety check
	ReservedIDs     []string

	// Targets is the preferred path for multi-target exchanges. When
	// non-empty, TargetOfferingID / TargetCount are ignored.
	Targets []TargetConfig

	// TargetOfferingID + TargetCount are the legacy single-target
	// fields, retained so pre-existing callers (HTTP handlers, sanity
	// tests, serialized PurchasePlans) don't need a flag-day change.
	// New code should populate Targets instead.
	TargetOfferingID string
	TargetCount      int32

	// DryRun here uses the AWS API DryRun parameter (permission check).
	// The quote call itself never performs an exchange.
	DryRun bool
}

// ExchangeExecuteRequest holds parameters for executing an exchange.
//
//nolint:revive // stutters, but ci_cd_sanity_tests references this type through pkg's pinned go.mod pseudo-version (not workspace-resolved under GOWORK=off); renaming here without a coordinated go.mod bump would break that build mode
type ExchangeExecuteRequest struct {
	Region          string
	ExpectedAccount string // optional safety check
	ReservedIDs     []string

	// Targets is the preferred path for multi-target exchanges. When
	// non-empty, TargetOfferingID / TargetCount are ignored.
	Targets []TargetConfig

	// Legacy single-target alias. Prefer Targets for new code.
	TargetOfferingID string
	TargetCount      int32

	// Guardrail: require PaymentDue <= MaxPaymentDueUSD to execute.
	// AWS returns a single aggregated PaymentDue across all targets,
	// so for multi-target requests this guardrail naturally becomes a
	// total cap rather than a per-target cap.
	// If nil, execution is refused.
	MaxPaymentDueUSD *big.Rat
}

// targetConfigs returns the ec2types slice to pass to the EC2 API.
// Prefers r.Targets when set; otherwise falls back to the legacy
// TargetOfferingID / TargetCount singleton.
func (r *ExchangeQuoteRequest) targetConfigs() []ec2types.TargetConfigurationRequest {
	return buildTargetConfigs(r.Targets, r.TargetOfferingID, r.TargetCount)
}

func (r *ExchangeExecuteRequest) targetConfigs() []ec2types.TargetConfigurationRequest {
	return buildTargetConfigs(r.Targets, r.TargetOfferingID, r.TargetCount)
}

func buildTargetConfigs(targets []TargetConfig, legacyOfferingID string, legacyCount int32) []ec2types.TargetConfigurationRequest {
	if len(targets) > 0 {
		out := make([]ec2types.TargetConfigurationRequest, 0, len(targets))
		for _, t := range targets {
			out = append(out, ec2types.TargetConfigurationRequest{
				OfferingId:    sdkaws.String(t.OfferingID),
				InstanceCount: sdkaws.Int32(t.Count),
			})
		}
		return out
	}
	return []ec2types.TargetConfigurationRequest{{
		OfferingId:    sdkaws.String(legacyOfferingID),
		InstanceCount: sdkaws.Int32(legacyCount),
	}}
}

// validateTargets returns a non-nil error if neither the Targets slice
// nor the legacy singleton fields carry a usable target offering, or if
// any target has a non-positive Count.
func validateTargets(targets []TargetConfig, legacyOfferingID string, legacyCount int32) error {
	if len(targets) > 0 {
		for i, t := range targets {
			if strings.TrimSpace(t.OfferingID) == "" {
				return fmt.Errorf("targets[%d].offering_id must be non-empty", i)
			}
			if t.Count <= 0 {
				return fmt.Errorf("targets[%d].count must be >= 1, got %d", i, t.Count)
			}
		}
		return nil
	}
	if strings.TrimSpace(legacyOfferingID) == "" {
		return fmt.Errorf("must provide target offering ID (either targets[] or target_offering_id)")
	}
	if legacyCount <= 0 {
		return fmt.Errorf("target_count must be >= 1, got %d", legacyCount)
	}
	return nil
}

// EC2ExchangeAPI defines the EC2 API methods used by exchange operations.
// Satisfied by *ec2.Client; accept this interface to enable testing without
// real AWS credentials.
type EC2ExchangeAPI interface {
	GetReservedInstancesExchangeQuote(ctx context.Context, params *ec2.GetReservedInstancesExchangeQuoteInput, optFns ...func(*ec2.Options)) (*ec2.GetReservedInstancesExchangeQuoteOutput, error)
	AcceptReservedInstancesExchangeQuote(ctx context.Context, params *ec2.AcceptReservedInstancesExchangeQuoteInput, optFns ...func(*ec2.Options)) (*ec2.AcceptReservedInstancesExchangeQuoteOutput, error)
}

// stsIdentityAPI defines the STS method used to verify ExpectedAccount.
// Satisfied by *sts.Client; unexported so a Client's identity resolver can
// only be set by construction (NewExchangeClient), never mixed and matched
// with an unrelated EC2 client after the fact -- see Client's doc comment.
type stsIdentityAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Client wraps an EC2ExchangeAPI for dependency-injected exchange
// operations. Use NewExchangeClient to construct one.
//
// ec2 and identity are set together at construction and never mutated
// afterward (no setter exists): both must resolve to the same AWS account,
// and a setter that let a caller swap one independently of the other would
// let the account guard pass while the exchange itself ran in a different
// account (issue #81 follow-up).
type Client struct {
	ec2      EC2ExchangeAPI
	identity stsIdentityAPI
}

// NewExchangeClient creates a Client from an AWS config. ec2 and identity
// are derived from the same config, so ExpectedAccount is honored on this
// Client's GetQuote/Execute methods just as it is on the package-level
// GetExchangeQuote/ExecuteExchange functions, and the two clients can never
// point at different accounts.
func NewExchangeClient(cfg sdkaws.Config) *Client {
	return &Client{ec2: ec2.NewFromConfig(cfg), identity: sts.NewFromConfig(cfg)}
}

// NewExchangeClientFromAPI creates a Client from an existing
// EC2ExchangeAPI implementation (useful for testing). It has no identity
// resolver: a request that sets ExpectedAccount on a Client built this way
// fails loud (see assertAccount) rather than silently skipping the check.
func NewExchangeClientFromAPI(api EC2ExchangeAPI) *Client {
	return &Client{ec2: api}
}

// GetQuote retrieves an exchange quote using the injected EC2 client.
func (c *Client) GetQuote(ctx context.Context, req ExchangeQuoteRequest) (*ExchangeQuoteSummary, error) {
	return getQuoteWithAPI(ctx, c.ec2, c.identity, req)
}

// Execute performs a convertible RI exchange with a spend-cap guardrail
// using the injected EC2 client.
func (c *Client) Execute(ctx context.Context, req ExchangeExecuteRequest) (string, *ExchangeQuoteSummary, error) {
	return executeWithAPI(ctx, c.ec2, c.identity, req)
}

func loadCfg(ctx context.Context, region string) (sdkaws.Config, error) {
	if strings.TrimSpace(region) == "" {
		return sdkaws.Config{}, fmt.Errorf("region must be specified explicitly; refusing to default to us-east-1 on an RI exchange path")
	}
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

// assertAccount verifies req.ExpectedAccount (when set) against the caller
// identity reported by identity.GetCallerIdentity. A nil identity resolver
// with a non-empty expected value fails loud rather than silently skipping
// the check (issue #81).
func assertAccount(ctx context.Context, identity stsIdentityAPI, expected string) error {
	if expected == "" {
		return nil
	}
	if identity == nil {
		return fmt.Errorf("cannot verify expected AWS account %q: no identity resolver configured for this exchange client", expected)
	}
	out, err := identity.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return err
	}
	if sdkaws.ToString(out.Account) != expected {
		return fmt.Errorf("unexpected AWS account: got %s want %s", sdkaws.ToString(out.Account), expected)
	}
	return nil
}

// GetExchangeQuote retrieves an exchange quote from the EC2 API.
func GetExchangeQuote(ctx context.Context, req ExchangeQuoteRequest) (*ExchangeQuoteSummary, error) {
	cfg, err := loadCfg(ctx, req.Region)
	if err != nil {
		return nil, err
	}
	return getQuoteWithAPI(ctx, ec2.NewFromConfig(cfg), sts.NewFromConfig(cfg), req)
}

// getQuoteWithAPI is the account-guarded entry point for a single quote:
// the choke point every caller (package-level wrappers and Client's
// methods) goes through, so ExpectedAccount is honored everywhere (issue
// #81). executeWithAPI calls quoteWithAPI directly instead, since it
// already checked the account once itself.
func getQuoteWithAPI(ctx context.Context, client EC2ExchangeAPI, identity stsIdentityAPI, req ExchangeQuoteRequest) (*ExchangeQuoteSummary, error) {
	if err := assertAccount(ctx, identity, req.ExpectedAccount); err != nil {
		return nil, err
	}
	return quoteWithAPI(ctx, client, req)
}

// quoteWithAPI performs the quote call using an EC2ExchangeAPI, without
// checking the account guard -- callers that already verified the account
// (executeWithAPI) use this to avoid a redundant STS round trip on the
// pre-accept re-quote.
func quoteWithAPI(ctx context.Context, client EC2ExchangeAPI, req ExchangeQuoteRequest) (*ExchangeQuoteSummary, error) {
	if len(req.ReservedIDs) == 0 {
		return nil, fmt.Errorf("must provide at least one reserved instance ID")
	}
	if err := validateTargets(req.Targets, req.TargetOfferingID, req.TargetCount); err != nil {
		return nil, err
	}

	in := &ec2.GetReservedInstancesExchangeQuoteInput{
		DryRun:               sdkaws.Bool(req.DryRun),
		ReservedInstanceIds:  req.ReservedIDs,
		TargetConfigurations: req.targetConfigs(),
	}

	out, err := client.GetReservedInstancesExchangeQuote(ctx, in)
	if err != nil {
		return nil, err
	}

	s := &ExchangeQuoteSummary{
		IsValidExchange:         sdkaws.ToBool(out.IsValidExchange),
		ValidationFailureReason: sdkaws.ToString(out.ValidationFailureReason),
		CurrencyCode:            sdkaws.ToString(out.CurrencyCode),
		PaymentDueRaw:           sdkaws.ToString(out.PaymentDue),
	}

	if out.OutputReservedInstancesWillExpireAt != nil {
		s.OutputReservedInstancesExp = out.OutputReservedInstancesWillExpireAt.Format("2006-01-02")
	}

	if s.PaymentDueRaw != "" {
		p, perr := ParseDecimalRat(s.PaymentDueRaw)
		if perr != nil {
			return nil, fmt.Errorf("quote returned invalid paymentDue %q: %w", s.PaymentDueRaw, perr)
		}
		s.PaymentDueUSD = p
		s.PaymentDueUSDStr = p.FloatString(6)
	}

	// Rollups (optional but useful for debugging)
	if out.ReservedInstanceValueRollup != nil {
		s.SourceHourlyPriceRaw = sdkaws.ToString(out.ReservedInstanceValueRollup.HourlyPrice)
		s.SourceRemainingUpfrontRaw = sdkaws.ToString(out.ReservedInstanceValueRollup.RemainingUpfrontValue)
		s.SourceRemainingTotalRaw = sdkaws.ToString(out.ReservedInstanceValueRollup.RemainingTotalValue)
	}
	if out.TargetConfigurationValueRollup != nil {
		s.TargetHourlyPriceRaw = sdkaws.ToString(out.TargetConfigurationValueRollup.HourlyPrice)
		s.TargetRemainingUpfrontRaw = sdkaws.ToString(out.TargetConfigurationValueRollup.RemainingUpfrontValue)
		s.TargetRemainingTotalRaw = sdkaws.ToString(out.TargetConfigurationValueRollup.RemainingTotalValue)
	}

	return s, nil
}

// ExecuteExchange performs a convertible RI exchange with a spend-cap guardrail.
// This is a convenience wrapper that creates its own AWS client from default config.
func ExecuteExchange(ctx context.Context, req ExchangeExecuteRequest) (exchangeID string, quote *ExchangeQuoteSummary, err error) {
	if req.MaxPaymentDueUSD == nil {
		return "", nil, fmt.Errorf("refusing to execute without max-payment-due-usd guardrail")
	}

	cfg, err := loadCfg(ctx, req.Region)
	if err != nil {
		return "", nil, err
	}

	return executeWithAPI(ctx, ec2.NewFromConfig(cfg), sts.NewFromConfig(cfg), req)
}

// ErrQuoteNotUSD is returned when a quote's CurrencyCode is not USD or is
// absent, since every exchange spend cap is denominated in USD.
var ErrQuoteNotUSD = errors.New("exchange quote is not denominated in USD")

// requireUSDPaymentDue returns the quote's PaymentDueUSD, refusing an absent
// amount (a zero-cost exchange parses to a non-nil zero) or a non-USD currency.
func requireUSDPaymentDue(q *ExchangeQuoteSummary) (*big.Rat, error) {
	if q.PaymentDueUSD == nil {
		return nil, fmt.Errorf("quote reported no PaymentDue; refusing to enforce the spend cap against an unknown amount")
	}
	switch q.CurrencyCode {
	case string(ec2types.CurrencyCodeValuesUsd):
		return q.PaymentDueUSD, nil
	case "":
		return nil, fmt.Errorf("%w: quote reported no CurrencyCode; refusing to enforce the USD spend cap", ErrQuoteNotUSD)
	default:
		return nil, fmt.Errorf("%w: quote is in %q; refusing to compare it against the USD spend cap", ErrQuoteNotUSD, q.CurrencyCode)
	}
}

// checkInitialQuote returns an error if the quote is invalid, carries no
// USD payment amount, or exceeds the spend cap.
func checkInitialQuote(q *ExchangeQuoteSummary, maxPayment *big.Rat) error {
	if !q.IsValidExchange {
		return fmt.Errorf("exchange is not valid: %s", q.ValidationFailureReason)
	}
	paymentDue, err := requireUSDPaymentDue(q)
	if err != nil {
		return err
	}
	if paymentDue.Cmp(maxPayment) == 1 {
		return fmt.Errorf("paymentDue %s exceeds max %s", paymentDue.FloatString(2), maxPayment.FloatString(2))
	}
	return nil
}

// checkReQuote returns an error if the pre-accept re-quote is invalid, carries
// no USD payment amount, or exceeds the cap. It is called immediately before
// AcceptReservedInstancesExchangeQuote to narrow the race window between
// pricing changes.
func checkReQuote(q *ExchangeQuoteSummary, maxPayment *big.Rat) error {
	if !q.IsValidExchange {
		return fmt.Errorf("exchange no longer valid at accept time: %s", q.ValidationFailureReason)
	}
	paymentDue, err := requireUSDPaymentDue(q)
	if err != nil {
		return fmt.Errorf("aborting exchange at accept time: %w", err)
	}
	if paymentDue.Cmp(maxPayment) == 1 {
		return fmt.Errorf(
			"aborting exchange: re-quoted payment %s USD exceeds cap %s USD (pricing changed between initial quote and accept)",
			paymentDue.FloatString(2),
			maxPayment.FloatString(2),
		)
	}
	return nil
}

// executeWithAPI performs the exchange using an injected EC2ExchangeAPI.
// It checks the account guard once itself, then uses the unchecked
// quoteWithAPI for both the initial quote and the pre-accept re-quote
// (issue #81 follow-up: avoids two redundant STS calls per Execute).
func executeWithAPI(ctx context.Context, client EC2ExchangeAPI, identity stsIdentityAPI, req ExchangeExecuteRequest) (string, *ExchangeQuoteSummary, error) {
	if req.MaxPaymentDueUSD == nil {
		return "", nil, fmt.Errorf("refusing to execute without max-payment-due-usd guardrail")
	}
	if err := assertAccount(ctx, identity, req.ExpectedAccount); err != nil {
		return "", nil, err
	}

	quoteReq := ExchangeQuoteRequest{
		Region:           req.Region,
		ExpectedAccount:  req.ExpectedAccount,
		ReservedIDs:      req.ReservedIDs,
		Targets:          req.Targets,
		TargetOfferingID: req.TargetOfferingID,
		TargetCount:      req.TargetCount,
		DryRun:           false,
	}

	q, err := quoteWithAPI(ctx, client, quoteReq)
	if err != nil {
		return "", nil, err
	}
	if checkErr := checkInitialQuote(q, req.MaxPaymentDueUSD); checkErr != nil {
		return "", q, checkErr
	}

	// Re-quote immediately before accepting to narrow the window between quote and
	// accept. The AWS API has no atomic quote+accept operation, so server-side
	// pricing can change between the two calls. This second quote reduces -- but
	// does not eliminate -- the race window. If the fresh quote now exceeds the
	// cap, abort before the irreversible accept call.
	freshQ, err := quoteWithAPI(ctx, client, quoteReq)
	if err != nil {
		return "", q, fmt.Errorf("pre-accept re-quote failed: %w", err)
	}
	if checkErr := checkReQuote(freshQ, req.MaxPaymentDueUSD); checkErr != nil {
		return "", freshQ, checkErr
	}

	out, err := client.AcceptReservedInstancesExchangeQuote(ctx, &ec2.AcceptReservedInstancesExchangeQuoteInput{
		ReservedInstanceIds:  req.ReservedIDs,
		TargetConfigurations: req.targetConfigs(),
	})
	if err != nil {
		return "", freshQ, err
	}

	return sdkaws.ToString(out.ExchangeId), freshQ, nil
}
