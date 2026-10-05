# Design: billing-account-scoped spend commitment recommendations

Status: proposed. Refs issue #78 (part d). Docs only: no production code is
changed by this document.

All code references are to `main` at `5acf38b`. Consumer-repo references are
to the default branches of `cloud-commitments-platform`, `-cli` and `-mcp` as
cloned on 2026-10-05.

## 1. Problem and constraints

### What the code does today

- `common.Recommendation` is per-resource. Identity is
  `Provider`/`Account`/`Service`/`Region`/`ResourceType`, and `ResourceType` is
  documented as "Instance type, node type, VM size, etc."
  (`pkg/common/types.go:191-277`, field at `:202`). There is no currency field
  and no field that names a scope other than `Account`.
- The GCP Memorystore, Cloud SQL and Cloud Storage clients set `ResourceType`
  from the last path segment of the first operation resource
  (`providers/gcp/services/memorystore/client.go:439`,
  `cloudsql/client.go:471`, `cloudstorage/client.go:448`; assigned at
  `memorystore/client.go:526`, `cloudsql/client.go:558`,
  `cloudstorage/client.go:535`). For Memorystore that is the instance name,
  which then fails tier pricing and `ValidateOffering`
  (`memorystore/client.go:237-250`, tiers at `:292-299`).
- Those clients query `google.memorystore.redis.PerformanceRecommender`
  (`memorystore/client.go:165`), `google.cloudsql.instance.PerformanceRecommender`
  (`cloudsql/client.go:170`) and `google.storage.bucket.CostRecommender`
  (`cloudstorage/client.go:167`). None of these is a commitment recommender.
  Cloud SQL's documents query, index and settings optimisation:
  <https://docs.cloud.google.com/sql/docs/mysql/recommender-create-indexes-join-settings>.
- All three `PurchaseCommitment` methods already return
  `common.ErrCommitmentPurchaseNotSupported` (`memorystore/client.go:223-234`,
  `cloudsql/client.go:231-241`, `cloudstorage/client.go:223-233`).
- The GCP provider is built around one project: `RecommendationsClientAdapter`
  holds a `projectID` (`providers/gcp/recommendations.go:73-77`), and
  `GetServiceClient` builds every client with `p.projectID`
  (`providers/gcp/provider.go:472-485`). `ProviderConfig` has `GCPProjectID`
  and nothing for a billing account (`pkg/provider/interface.go:91`).
- Compute Engine uses the resource-based `UsageCommitmentRecommender`
  (`computeengine/client.go:411`) and is out of scope.

### What Google documents

- Spend-based CUD recommendations "are available only on the Google Cloud
  console". The covered services include Memorystore and Cloud SQL, not Cloud
  Storage. Viewing them requires roles on the Cloud Billing account:
  <https://docs.cloud.google.com/docs/cuds-recommender>.
- The recommenders page lists `google.cloudbilling.commitment.SpendBasedCommitmentRecommender`
  ("Spend-based committed use discount recommender") in one table with columns
  Category, Name, ID, Description and BigQuery export; the ID appears twice
  and the table has no resource-type or scope column:
  <https://docs.cloud.google.com/recommender/docs/recommenders>. The
  billing-account scope is inferred, not documented there: it rests on the
  `google.cloudbilling` ID namespace and on the Go SDK accepting a
  `billingAccounts/[BILLING_ACCOUNT_ID]/locations/[LOCATION]/recommenders/[RECOMMENDER_ID]`
  parent (`cloud.google.com/go/recommender@v1.13.6/apiv1/recommenderpb/recommender_service.pb.go:344`).
  That pattern is generic to all recommenders.
- The recommendation body that would carry the commitment amount is
  `RecommendationContent.Overview`, typed `*structpb.Struct`
  (`recommenderpb/recommendation.pb.go:486`). Its keys for this recommender are
  not documented. The cost impact is typed. `CostProjection.Cost` is a
  `google.type.Money` with a currency code, and "negative cost units indicate
  cost savings and positive cost units indicate increase"; `CostProjection`
  also has `CostInLocalCurrency`, "the approximate cost savings in the billing
  account's local currency" (`recommendation.pb.go:869-885`). The generic
  operation contract is at
  <https://docs.cloud.google.com/recommender/docs/key-concepts>.
- Spend-based commitments are bought per billing account with
  `billingAccounts/BILLING_ACCOUNT_ID/orders:place`, choosing an offer per
  product and term, with a `commitment_amount` (or `hourly_commit`) parameter
  and a region for regional commitments. Cloud SQL and Memorystore for Redis
  are listed; Cloud Storage is not:
  <https://docs.cloud.google.com/marketplace/docs/commitment-api-purchasing>.
  The page does not say which currency `commitment_amount` is in.

### Constraints from the owner (issue #78, 2026-09-30 comment)

- Do not invent payload fields, `/tier` paths, or tiers.
- Returning errors for everything, or suppressing output, does not resolve the
  issue. An authoritative source and payload must be established, then the
  whole path implemented and verified.

### Consequence

A GCP spend-based recommendation is not a resource with a tier. It is: one
billing account, one service, one region, one term, one hourly amount in one
currency. The current type can only express it by abusing `Account` and
`ResourceType`.

## 2. Options for the type model

### Option A: extend `Recommendation` with a scope and a spend details type (recommended)

Keep `Recommendation` as the single envelope. Add an optional typed `Scope`,
a new `CommitmentType`, new service slugs and a new `ServiceDetails`
implementation that carries the hourly amount and currency.

This is the existing Savings Plans precedent, generalised. AWS Savings Plans
are already a `Recommendation` with `Count: 1`, no `ResourceType`, and the
dollar quantity in `Details` (`providers/aws/recommendations/parser_sp.go:231-254`).
`ScaleRecommendationCosts` (`pkg/common/types.go:317-336`) and `ApplyCoverage`
(`pkg/recfilter/sizing.go:47-55`) already special-case that shape.

Every `[]common.Recommendation` signature stays
(`pkg/provider/interface.go:38-66`) and the details codec already dispatches
by service slug (`pkg/common/service_details_codec.go:121`). The cost: every
consumer keying on `Account`/`ResourceType` must key spend recs on `Scope`
and the details (section 5).

### Option B: a separate `SpendCommitmentRecommendation` type behind a shared interface

No spend rec can be misread as per-resource, but `ServiceClient`,
`RecommendationsClient`, the scorer, `recfilter`, the reporter, the audit
record and all three consumers take `[]common.Recommendation`. Splitting
changes every signature and duplicates scoring and sizing, for the same
outcome as Option A.

### Option C: reuse `SavingsPlanDetails` and the Savings Plans slugs as they are

`SavingsPlanDetails` is AWS-shaped (`PlanType`, `InstanceFamily`,
`OfferingID` from Cost Explorer; `pkg/common/types.go:645-667`), and
`IsSavingsPlan` drives AWS-only branches (MCP
`tools/search_recommendations.go:205,351`). It has no scope and no currency,
so the billing account would go in `Account` and USD would be implied. The
Azure savings plan client shows that cost: it hardcodes a subscription scope
and `USD` (`providers/azure/services/savingsplans/client.go:268`, `:282`).

### Recommendation

Option A. Reuse the `Recommendation` envelope and the Savings Plans
conventions (`Count: 1`, no `ResourceType`, dollar quantity in `Details`,
scaled by `ScaleRecommendationCosts`), but with an explicit `Scope` and a
spend-specific details type, instead of overloading `Account` and
`SavingsPlanDetails`.

What breaks without it:

- Without `Scope`, the platform collects GCP per project
  (`cloud-commitments-platform/internal/scheduler/scheduler.go:867`), so one
  billing-account recommendation would be stored once per project under that
  billing account, and its identity would name a project that cannot buy it.
- Without a spend-denominated branch in sizing, `ApplyCoverage` sends a
  `Count: 1` rec down the RI path, where `int(1 * ratio)` is 0 and the rec is
  dropped (`pkg/recfilter/sizing.go:68-77`).
- Without a currency, a non-USD billing account's savings would be summed as
  dollars (`pkg/reporter/reporter.go:36`, `:100` print `$`).

## 3. Proposed types (sketch, not committed code)

```go
// pkg/common

// ScopeKind names what a commitment is bought against.
type ScopeKind string

const ScopeBillingAccount ScopeKind = "billing-account"

// CommitmentScope is set only on recommendations whose purchase scope is not
// Recommendation.Account. nil means the existing per-account meaning.
type CommitmentScope struct {
    Kind ScopeKind `json:"kind"`
    // ID is the provider resource name, e.g. "billingAccounts/012345-6789AB-CDEF01".
    ID string `json:"id"`
}

func (s CommitmentScope) Validate() error // unknown Kind or empty/malformed ID -> error

// Recommendation gains one field:
//     Scope *CommitmentScope `json:"scope,omitempty" csv:"-"`

const CommitmentSpendCUD CommitmentType = "spend-committed-use"

// Per-service slugs, mirroring the per-plan-type Savings Plans slugs.
const (
    ServiceSpendCUDCloudSQL    ServiceType = "spend-cud-cloudsql"
    ServiceSpendCUDMemorystore ServiceType = "spend-cud-memorystore"
)

func IsSpendCommitment(s ServiceType) bool

// IsSpendDenominated reports whether a rec is sized by an hourly amount
// rather than Count: Savings Plans and spend CUDs.
func IsSpendDenominated(rec Recommendation) bool

// SpendCommitmentDetails is the Details for CommitmentSpendCUD.
type SpendCommitmentDetails struct {
    // HourlyAmount is the recommended commitment per hour in Currency.
    HourlyAmount float64 `json:"hourly_amount"`
    // Currency is the ISO 4217 code from the payload, never defaulted.
    Currency string `json:"currency"`
    // OfferID is the Consumer Procurement offer for this service and term.
    // nil until resolved; the purchase path refuses a nil OfferID.
    OfferID *string `json:"offer_id,omitempty"`
}

func (d SpendCommitmentDetails) GetServiceType() ServiceType // ServiceCommitments
func (d SpendCommitmentDetails) GetDetailDescription() string

// Typed errors.
var ErrUnsupportedSpendService = errors.New("service has no spend-based commitment")
var ErrUnsupportedCurrency = errors.New("currency not supported by this consumer")

type SpendPayloadError struct {
    Recommendation string // recommender resource name
    Field          string // which required value was missing or invalid
    Err            error
}
```

Where each value lives on a spend rec:

| Value | Field | Absent |
| --- | --- | --- |
| Billing account | `Scope{Kind: ScopeBillingAccount, ID}` | parser error, no rec |
| Billing account ID (bare) | `Account` | parser error; see "Account and filters" in section 5 |
| Service | `Service` (spend slug) | `ErrUnsupportedSpendService` |
| Region | `Region` | parser error (regional commitments only at first) |
| Term | `Term` ("1yr"/"3yr") | parser error; no "1yr" default |
| Hourly amount | `Details.HourlyAmount` | parser error |
| Currency | `Details.Currency` | parser error; never assumed USD. `Cost` and `CostInLocalCurrency` may differ in currency, so the parser must pick one explicitly (open question 2) |
| Savings | `EstimatedSavings` from typed `CostProjection.Cost`, negated (negative units are savings) | parser error |
| Offer | `Details.OfferID` | nil, purchase refused |
| Instance tier | `ResourceType` stays empty | by design, as for Savings Plans |

A missing required value yields a `*SpendPayloadError` and no
`Recommendation`; a run where every payload fails is a failure, not "no
savings" (same guard as `providers/gcp/recommendations.go:191`).
`RecurringMonthlyCost` stays nil unless the payload states it. `Count` is 1
and sizing scales `HourlyAmount`. The service mapping is a closed switch:
Cloud SQL and Memorystore for Redis map to the two slugs, anything else
(including Cloud Storage) returns `ErrUnsupportedSpendService`.

## 4. The parser boundary and the evidence gap

### Isolation

A new package `providers/gcp/services/spendcud` owns the billing-account
client. Fetching and parsing are separate:

```go
// providers/gcp/services/spendcud

type Client struct {
    billingAccount common.CommitmentScope // Kind == ScopeBillingAccount
    recommender    RecommenderClient      // same narrow interface as memorystore/client.go
}

// GetRecommendations lists ACTIVE recommendations under
// billingAccounts/{id}/locations/{loc}/recommenders/google.cloudbilling.commitment.SpendBasedCommitmentRecommender
// and passes each one to parseSpendRecommendation.
func (c *Client) GetRecommendations(ctx context.Context, p *common.RecommendationParams) ([]common.Recommendation, error)

// parseSpendRecommendation reads the typed parts of the proto (name, state,
// CostProjection.Cost and its currency) and delegates the untyped Overview to
// parseOverview.
func parseSpendRecommendation(scope common.CommitmentScope, rec *recommenderpb.Recommendation) (common.Recommendation, error)

// overviewFields is everything this design needs from Overview.
type overviewFields struct {
    Service      common.ServiceType
    Region       string
    Term         string
    HourlyAmount float64
    Currency     string
}

// parseOverview is the only code that reads structpb keys.
// Until a real payload is captured it returns ErrOverviewSchemaUnverified.
func parseOverview(o *structpb.Struct) (overviewFields, error)

var ErrOverviewSchemaUnverified = errors.New("spend CUD overview schema not yet verified against a real payload")
```

Wiring a real payload later changes only `parseOverview` and its golden
fixture tests. The types in section 3, the client, the collector and every
consumer stay as they are.

While `parseOverview` returns `ErrOverviewSchemaUnverified`, the client
returns that error from `GetRecommendations` rather than an empty slice. Spend
collection is a separate call, not one more leg of the per-region errgroup in
`providers/gcp/recommendations.go`: there `collectRegion` (`:296-345`) logs
each per-service error at WARN and `mergeRegionResults` (`:191-214`) errors
only when every service call failed, so the unverified error would be
swallowed whenever Compute Engine succeeded. The separate call returns its
error to the caller, which must surface it instead of reporting "no
recommendations". This is the honest state until evidence exists.

### Evidence still needed (parser is blocked)

1. The recommender parent scope: that
   `SpendBasedCommitmentRecommender` is served under
   `billingAccounts/...` (inferred today, see section 1), and not, for
   example, under projects or organizations. The same capture confirms it.
2. A real `ListRecommendations` response for
   `SpendBasedCommitmentRecommender` from a billing account with Cloud SQL or
   Memorystore spend, captured as JSON (`protojson`), with account IDs
   redacted. A capture tool in `ci_cd_sanity_tests` (cloud-backed, run only on
   purpose) is step 4 below. Alternatively, written confirmation of the
   schema from Google.
3. From that sample, or from Google:
   - the `Overview` keys for service, region, term, hourly amount and
     currency, and whether the amount is hourly or a different grain;
   - which `[LOCATION]` the recommender uses (`global` or per region);
   - whether the API returns these at all, given the console-only statement;
   - whether one recommendation can cover several regions or services;
   - over what `Duration` `CostProjection.Cost` applies, and whether `Cost`
     or `CostInLocalCurrency` matches the currency of the amount in
     `Overview`;
   - the IAM role needed on the billing account;
   - how a recommendation maps to a Consumer Procurement offer (by product
     and term, per the purchasing page tables) and which currency
     `commitment_amount` uses.
4. Whether Memorystore's recommendations are Redis-only or also cover
   Valkey or Memcached, since only "Memorystore for Redis" is on the purchase
   list.

## 5. Blast radius

### This module

| Site | Change |
| --- | --- |
| `pkg/common/types.go:191-277` | add `Scope`; add constants, details type, errors |
| `pkg/common/types.go:317-336` `ScaleRecommendationCosts` | scale `SpendCommitmentDetails.HourlyAmount` |
| `pkg/common/service_details_codec.go:121` | map the spend slugs to `SpendCommitmentDetails` |
| `pkg/common/matches.go:7-21` `Matches` | spend recs match on scope, service, region, term |
| `pkg/recfilter/sizing.go:47-55`, `:202` | branch on `IsSpendDenominated` instead of `IsSavingsPlan` |
| `pkg/recfilter/dedupe.go:178` | spend recs skip the `ResourceType` pool key |
| `pkg/scorer/scorer.go:65-66` | tie-break includes scope ID and hourly amount |
| `pkg/reporter/reporter.go:36`, `:100` | print the currency instead of `$` for spend recs |
| `pkg/common/audit.go:293-297` | record scope ID and currency |
| `pkg/provider/interface.go:91` | billing account source (see open question 1) |
| `providers/gcp/provider.go:472-485` | route spend slugs to `spendcud` |
| `providers/gcp/recommendations.go:291` | collect spend recs once per billing account, not per region, as a separate call whose error propagates |
| `pkg/recfilter/filters.go:55-65` `Filters.IncludesInstanceType` | decision below: spend recs bypass the instance-type filter |
| `memorystore`, `cloudsql`, `cloudstorage` clients | stop emitting recs from non-commitment recommenders once the spend path is live |
| AWS and Azure | no change; Savings Plans keep their slugs and details |

### cloud-commitments-platform

- DB: `recommendations` has a unique natural key on
  `(account_key, provider, service, region, resource_type, engine, term, payment_option)`
  (`internal/database/postgres/migrations/000043_recommendations_add_engine_to_key.up.sql:47-48`).
  `account_key` derives from `cloud_account_id`, which is one GCP project.
  A migration adds `scope_kind`, `scope_id` and `currency` columns and
  rebuilds the key so spend recs key on scope instead of project.
- Record and identity: `RecommendationRecord`
  (`internal/config/types.go:506`) gains scope and currency;
  `recommendationID` (`internal/scheduler/scheduler.go:1061`),
  `recIdentityKey` (`internal/api/purchase_pricing.go:122`) and the
  suppression key use `ResourceType` today and need the scope.
- Collection: `collectGCPRecommendations` (`scheduler.go:867`) fans out per
  project. Spend recs need one collection per billing account, deduped by
  `Scope.ID`; there is no billing-account entity today.
- Purchase: execution builds the client with
  `GetServiceClient(serviceType, rec.Region)`
  (`internal/purchase/execution.go:1060`). Until purchase exists the spend
  client returns `ErrCommitmentPurchaseNotSupported`; the platform does not
  reference that sentinel today, so approvals of spend recs must be blocked
  explicitly.
- API and UI: `frontend/src/api/types.ts:125,272,471` declare
  `resource_type: string`; the UI needs scope, currency and an hourly-amount
  column for spend rows. Email templates render `{{.Count}}x {{.ResourceType}}`,
  which prints an empty type for spend recs. Spend caps and dashboard totals
  assume USD. In `internal/api/openapi.yaml` (platform main `115ff70`)
  `resource_type` is an optional string with no `minLength` in
  `RecommendationRecord` (`:2665`), `PlannedPurchase` (`:3015`) and
  `PurchaseHistoryRecord` (`:3092`), so an empty value is valid. Scope and
  currency fields still need adding.

### cloud-commitments-cli

The CSV header (`cmd/multi_service_csv.go:244-247`) needs scope, currency and
hourly-amount columns; the cap sort key (`cmd/multi_service_csv_cap.go:108`)
needs the scope; `IsSavingsPlan` branches such as the count override
(`cmd/helpers_count_override.go:49`) should use `IsSpendDenominated`.

`passesDimensionFilters` (`cmd/multi_service_filters.go:95-105`) calls
`shouldIncludeInstanceType(rec.ResourceType, cfg)` and
`shouldIncludeAccount(rec.AccountName, cfg)`. With `--include-instance-types`
or `--include-accounts`, every spend rec (empty `ResourceType`, empty account)
is silently dropped; `recfilter.Filters.IncludesInstanceType` has the same
behaviour for any caller. `AccountName` is filled from `Account` through the
alias cache (`cmd/multi_service_helpers.go:203`, `:499`).

### Account and filters (decision)

AWS Savings Plans recommendations set `Account` (`parser_sp.go:245`). Leaving
`Account` empty for spend recs would depart from that pattern. Decision:
populate `Account` with the billing account ID (the bare ID, `Scope.ID` keeps
the resource name), so name-based account filters and the alias cache keep
working, and keep `Scope` as the authoritative purchase scope. Consumers that
group by `Account` must key spend recs on `Scope` (section 5). The instance
type filter does not apply to spend recs, since they have no instance type:
`IncludesInstanceType` and the CLI check skip them via `IsSpendDenominated`,
and `--exclude-instance-types` likewise cannot match them. Step 2 covers both,
with a test that an include-instance-types filter keeps a spend rec.

### cloud-commitments-mcp

`search_recommendations` serialises `common.Recommendation` directly, so new
fields appear without code; its SP branches are AWS-only
(`tools/search_recommendations.go:205`, `:351`). `detailsKeyComponent`
(`tools/purchase.go:472`, used by `idempotencyKeyFor` at `:457`) switches on
concrete details types: without a `SpendCommitmentDetails` case, spend recs
with different amounts share an idempotency key.

All three consumers pin pseudo-versions of `pkg` and `providers/gcp` with no
`replace`, so each needs a version bump after the library steps land.

## 6. Migration plan

Each step is one PR under about 400 lines and can ship alone.

1. **Types in `pkg/common`.** `CommitmentScope`, `ScopeKind`,
   `CommitmentSpendCUD`, spend slugs, `SpendCommitmentDetails`, errors,
   `IsSpendCommitment`, `IsSpendDenominated`, codec mapping. No producers.
   Verify: unit tests for `Validate`, codec round trip, JSON omits nil
   `Scope`; existing tests unchanged.
2. **Shared consumers in this module.** Sizing, scaling, dedupe, matching,
   scorer tie-break, reporter currency, audit fields. Verify: table tests
   with a spend rec through `ApplyCoverage` (amount scaled, not dropped),
   an include-instance-types filter that keeps the rec,
   `ApplyTargetCoverage`, `AdjustRecommendationsForExisting`, reporter output
   with a non-USD currency; a deletion probe on each branch.
3. **`spendcud` client with the blocked parser.** Billing-account fetch,
   `parseSpendRecommendation` on typed fields, `parseOverview` returning
   `ErrOverviewSchemaUnverified`, `PurchaseCommitment` returning
   `ErrCommitmentPurchaseNotSupported`, provider routing. Depends on open
   question 1: `GetServiceClient` has only a project and a region
   (`providers/gcp/provider.go:472-485`), so routing cannot ship until the
   billing account source is decided. That decision changes
   `pkg/provider/interface.go:91` (`ProviderConfig`, if a config field is
   chosen) and the collection entry in `providers/gcp/recommendations.go:291`.
   Verify: fake recommender tests for pagination, state filter, error
   propagation, that the client returns the unverified error rather than an
   empty slice, and a provider-level test proving the error reaches the caller
   of spend collection while another service succeeds.
4. **Evidence capture.** A diagnostic in `ci_cd_sanity_tests` that lists the
   recommender for a given billing account and writes redacted `protojson`.
   Run once against a billing account with Cloud SQL or Memorystore spend,
   with the owner's go-ahead. Verify: the captured file is attached to #78.
5. **Real `parseOverview`.** Implement against the captured keys only, with
   the sample as a golden fixture and negative fixtures for each missing
   field. Verify: fixture tests; a live run of the step 4 tool that parses
   every returned recommendation. Blocked on step 4.
6. **Retire the non-commitment GCP paths.** Remove the Performance and Cost
   recommender queries from Memorystore, Cloud SQL and Cloud Storage
   recommendation collection, so issue #78's defect cannot recur. Verify:
   the collector emits only Compute Engine resource CUDs and spend CUDs.
7. **Consumers.** Platform migration and record/key changes, then collection
   per billing account, then UI; CLI columns; MCP idempotency key. Each is
   its own PR in its own repo. Verify: platform migration up/down test,
   end-to-end collection with a fixture provider, Playwright on the
   recommendations page.
8. **Purchase via Consumer Procurement.** A separate design: offer
   resolution, `orders.place`, idempotency, and existing-commitment listing.
   Out of scope here.

## 7. Open questions for the owner

1. Billing account source: add `GCPBillingAccountID` to `ProviderConfig`, or
   resolve it from the project with Cloud Billing `projects.getBillingInfo`
   (already a dependency via `google.golang.org/api/cloudbilling/v1`)? The
   first is explicit; the second needs no config but needs billing IAM on
   each project.
2. Currency: should step 2 make consumers currency-aware, or should the GCP
   parser reject non-USD with `ErrUnsupportedCurrency` until the platform's
   caps and totals handle currency?
3. Platform model: add a billing-account entity, or store spend recs with a
   NULL `cloud_account_id` and key them on `scope_id`?
4. Who runs step 4, against which billing account, and may the redacted
   sample be committed as a test fixture?
5. Should step 6 wait for step 5, so GCP Cloud SQL and Memorystore show no
   recommendations in the meantime, or ship earlier with the unverified-error
   state visible in the platform?
6. Should the Azure savings plan client's hardcoded subscription scope and
   USD (`providers/azure/services/savingsplans/client.go:268`, `:282`) move to
   `CommitmentScope` and the details currency in a later change?
