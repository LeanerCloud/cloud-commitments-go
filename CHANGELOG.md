# Changelog

All notable changes to CUDly are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Notices

- **Federation IaC bundles downloaded before 2026-04-22 need to be
  re-downloaded** to get zero-touch registration. Older bundles silently
  skip auto-registration unless manually edited (Terraform `registration.tf`
  gated `do_register` on `cudly_api_url`; CLI shell scripts included the
  registration call only when `CUDlyAPIURL` was present at render time;
  CloudFormation deploy scripts had no registration call at all).
  Re-download the bundle from the CUDly UI and the new copy will register
  your account automatically with no manual edits required.
- **Federation IaC bundles deployed before #1219 need to be re-applied** to
  pick up reconciled IAM action grants. Older bundles silently degrade on
  federated accounts: the Cost Explorer `Get*Coverage` actions are missing
  (coverage-targeted sizing assumes zero existing coverage), and the
  cross-account CloudFormation flavor lacks the optional `EnableOrgDiscovery`
  parameter and the legacy CE statement that the original `CUDly-CrossAccount`
  template carried. Re-download the federation bundle from the CUDly UI and
  re-apply (`terraform apply` / `aws cloudformation update-stack`) -- no
  manual edits required, no changes to existing CUDly resources. Customers
  on the runtime CloudFormation stack or Terraform lambda/fargate modules
  also need an update to pick up the new `ec2:*ReservedInstancesExchangeQuote`
  actions, `ec2:DescribeRegions`, `rds:DescribeDBInstances`, and the
  new-style `es:*ReservedInstance*` OpenSearch actions (replacing the legacy
  `es:*ReservedElasticsearch*` names).

### Added

- **`recommendations.AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions`:**
  the per-pool (EC2 and other non-family) expiry deduction, plus credit for
  replacements already bought. Coverage is a lookback average, so RIs bought
  inside `CoverageLookbackDays` are not in it yet; with R the count of active,
  non-expiring RIs in the same pool that started inside the lookback, the
  deduction is `max(0, expiring - R)`. It can only buy less, never more. A
  growth purchase in the pool counts as a replacement, and a replacement of
  a different size in the same family is not credited.
  `CoverageLookbackDays <= 0` credits none. Additive; the existing functions
  are unchanged (#346).

- **`recommendations.Client.GetSPCommitmentRate`:** commitment dollars per
  on-demand dollar (r) of the used part of a new Savings Plan, derived from CE
  purchase recommendations as `(utilization/100) * EstimatedSPCost /
  (EstimatedSPCost + EstimatedSavingsAmount)` (ratio of sums over matching
  details). Strict raw-detail parsing; an absent field or no matching detail is
  `ErrNoSPCommitmentRate`, never a 1:1 fallback. Nothing consumes it yet
  (go#349, groundwork for converting on-demand ladder sizing to commitment
  dollars).

- **`recommendations.ApplyFamilyNUSizingRDSWithExpiryOptions`:** the
  rebuy-window RDS family sizing, plus credit for replacements already bought.
  Coverage is a lookback average, so RIs bought inside `CoverageLookbackDays`
  are not in it yet; with R the NU of visible active, non-expiring RIs that
  started inside the lookback, post-expiry covered is
  `max(0, max(min(C, C - E + R), min(C, A - E)))`. It is never below the R = 0
  formula, so it can only buy less, and a partial inventory still never removes
  more than expires. `FamilyExpiryReport.ReplacementNU` records R.
  `CoverageLookbackDays <= 0` credits none. Additive; the other functions are
  unchanged (LeanerCloud/cloud-commitments-cli#2132, #2175).
- **`common.SavingsPlanDetails.Currency`:** the ISO 4217 code the hourly
  commitment is denominated in. Additive and unused until the Azure Savings Plan
  client starts requiring it (#46).

- **`recommendations.ApplyFamilyNUSizingRDSWithExpiry`:** family-NU sizing
  that deducts RDS RIs expiring inside the rebuy window from family coverage
  in normalized units (`max(covered - expiring, min(covered, active - expiring))`,
  so a partial RI inventory never removes more than expires), and reports
  expiring RIs it could not use (`FamilyExpiryReport.Unusable`). Additive;
  `ApplyFamilyNUSizingRDS` is unchanged. Consumers that pass a rebuy window
  will see higher RDS counts and costs for families with expiring RIs
  (LeanerCloud/cloud-commitments-cli#2132).
- **`insurance.ContractTerms()`:** returns the documented Archera
  `contract_term` values that `Comparison` accepts and the decoder returns, as a
  new sorted slice on every call (#316). The order is lexicographic, not by
  duration. Consumers should call it instead of copying the list.
- **`common.ErrOutcomeUnknown`:** exported sentinel for a purchase whose
  response was lost and whose commitment may exist (#301). Consumers can use
  `errors.Is` to tell it from a definite failure. The AWS EC2 and Redshift
  clients switch to it in a follow-up once the aws module pins this commit.
- **`pkg/insurance`:** a read-only Archera insured-commitment comparison
  contract (#284, #285). `NewClient` returns a client with `Comparison` and
  `Plan` for Archera's documented
  `GET /v1/org/{org_id}/commitment-plans/{plan_id}/comparison` and
  `GET /v1/org/{org_id}/commitment-plans/{plan_id}` endpoints (`DecodeComparison`
  and `DecodePlan` parse already-fetched bodies). The origin is fixed at
  `https://api.archera.ai`, the key is sent only in the `x-api-key` header, and
  the client never follows redirects and never retries. It has no purchase or
  binding capability and nothing in the repo calls it yet. Contract points
  consumers must keep:
  - Money is `*big.Rat`; nil means unknown, never zero. `archera_premium` is
    already inside `commitment_cost.total`, so do not add it again. Monthly
    rates are 730-hour rates and the one-time upfront cost is a separate field;
    do not sum them.
  - The vendor schema has no currency or expiry field, so both stay nil
    (unknown). `FetchedAt` records retrieval time and freshness policy is the
    consumer's.
  - Nullable vendor identity (term, payment option, commitment type) stays
    nullable. Each comparison row keeps its current offer and all candidate
    offers.
  - A comparison is a hypothetical rollup, not a bindable quote. An insured
    target of 100% is a requested target subject to Archera underwriting, not
    a guarantee.
  - `AssessProductSupport` returns `supported` only for the two AWS rows quoted
    from Archera's Supported Reservable Services page (`aws/AmazonEC2` and
    `aws/savingsplan/Compute`); every other product is `unknown`. Support is
    not underwriting eligibility.
  - The API key is redacted from `fmt` output for `Config` and `Client`,
    except `%p` (and `%T` on a non-pointer value), which `fmt` handles before
    redaction.
  - Verified with offline tests only; no live Archera call has been made, so
    response shapes come from the published schema.

### Changed

- **BREAKING `gcp`: Cloud SQL and Memorystore no longer return CUD
  recommendations (#294).** They came from the project-scoped
  `PerformanceRecommender`s, which give per-instance tuning advice, and were
  relabeled as 1yr CUDs with made-up or zero savings. Cloud SQL and
  Memorystore CUDs are spend-based, and Google recommends them only at Cloud
  Billing account scope (see `docs/design/gcp-spend-commitments.md`).
  `GetSupportedServices` now returns only `ServiceCompute`, the default
  recommendation fan-out queries only the Compute Engine commitment
  recommender, and an explicit Cloud SQL or Memorystore request returns an
  error wrapping `common.ErrCommitmentPurchaseNotSupported` without calling
  the Recommender API. `SetRecommenderClient` on both clients is now a
  deprecated no-op. Pricing (`GetOfferingDetails`, `ValidateOffering`) is
  unchanged.

- **BREAKING `ladder.Allocate` converts the on-demand-equivalent gap to
  commitment dollars (go#349).** Tranches were bought 1:1 against an
  on-demand-priced gap, over-buying by 1/rate (a 30% discount bought 43% too
  much). `AllocationInput.InFlightUSDPerHour *float64` is replaced by
  `InFlight []InFlightTranche` (commitment USD/hr plus the rate stored with
  each tranche, netted as commitment/rate) and a required `CommitmentRates`
  map (a partial map is fine: a layer that receives an allocation without a
  rate is held, never bought 1:1; a rate outside (0, 1] or non-finite is an
  error). `Allocation.GapUSDPerHour` is now commitment USD/hr, with
  `OnDemandEquivalentUSDPerHour` and `Rate` alongside; the per-run cap applies
  to commitment dollars. The base split now also counts in-flight base
  commitment against the stable level. `BuildTranches` rejects an `Allocation`
  without a rate in (0, 1] and stamps it on `Tranche.CommitmentRate`. The
  amounts are exact; the purchase boundary must round commitment DOWN.
  Tranches persisted before this change are in the old unit and carry no
  rate. Consumer: the platform's `internal/server/handler_ladder.go` only.

- **BREAKING `ladder.TrancheResult.BuyNow` removed; `BuildTranches` now emits a
  `Tranche` for every ramp step, including `AfterDays == 0`** (its `FireAfter`
  equals `TrancheInput.Now`). The immediate step was returned as a `BuyNow`
  action that the platform never persisted, so in-flight netting could not see
  it (#36). The only consumer is the platform's `internal/server/handler_ladder.go`;
  cloud-commitments-cli and cloud-commitments-mcp do not reference it. Buy-now
  rationale and data sources are no longer carried on the immediate step
  (tranches have no such fields). `Tranche.AmountUSDPerHour` and
  `AllocationInput.InFlightUSDPerHour` now document their unit (#349).

- **Breaking (source):** `insurance.Config.APIKey` is now an opaque
  `insurance.Secret` (build it with `insurance.NewSecret(key)`) instead of a
  `string`. The key lives in a closure, so a `Config` held in an unexported
  struct field no longer prints it under any fmt verb, json, xml, yaml or slog.
  `Config.Format` is removed (`%v` of a `Config` now prints
  `{[redacted] <org>}`), and two copies of one `Config` are no longer
  `reflect.DeepEqual`. `Config` is also no longer comparable (`==` stops
  compiling and a `Config` map key panics as unhashable), and
  `json.Unmarshal` into a `Config` carrying an `APIKey` now errors where the
  old `json:"-"` tag ignored it (#330)
- **Breaking (source):** `common.PurchaseResult` gained an `ExistingCommitment`
  field. Downstream code that builds it with unkeyed composite literals no
  longer compiles; keyed literals are unaffected. The field is true when a
  provider adopted a commitment that already existed for the same idempotency
  token (#211)

### Fixed

- **`aws/recommendations`:** RDS family-NU sizing and the expiry deduction no longer pool SQL Server by instance family (its RIs are never size-flexible): those recs go to the per-size path and expiring SQL Server RIs count as `Unusable`. Oracle stays pooled until the license model is carried (#342).
- **`aws/recommendations`:** `FamilyExpiryReport.ExpiringNU` now reports the NU actually removed from family covered NU (before minus after) instead of the expiring RIs' own NU, so it is 0 when underutilized RIs absorb the expiry (#342).
- **`azure`:** the Retail Prices `$filter` values built from SKU and region in
  the search, compute, cosmosdb and savingsplans clients are now validated with
  `pricing.ODataString`, so an injected value is rejected before any request is
  built (#526).
- `reporter.RenderSummary` now lists filtered-reason categories in sorted order instead of random map order (#110).
- **`gcp/computeengine`:** a Recommender cost projection with a non-zero cost
  and a non-USD or missing currency code is now skipped with a logged warning
  instead of being reported as USD `EstimatedSavings`.
- **`azure/compute`:** reservation exchange targets are no longer always
  billed Upfront. `ExchangeTarget.BillingPlan` (new, optional) is sent as each
  target's `billingPlan` in the CalculateExchange body, so the session that
  `ExecuteExchange` commits carries it. Unset keeps the previous Upfront
  request byte for byte; anything other than `Upfront` or `Monthly` (exact
  spelling) is a validation error before any request (#89).

- **`azure/savingsplans`:** the commitment currency is no longer hardcoded to
  USD. `SavingsPlanDetails.Currency` (new, additive) is sent in the purchase and
  validate bodies and returned by `GetOfferingDetails`; empty or malformed
  values (anything but 3 ASCII letters; format-only, not ISO 4217 membership)
  fail closed (#46).

- **`pkg/insurance`:** hardened vendor-text handling in `Client` errors
  (#286). The sanitizer now also strips format, line and paragraph separator
  characters (Unicode Cf, Zl, Zp) from vendor text; other invisible characters
  (for example U+034F, variation selectors, U+3164) are not stripped and can
  still split an echoed key, which is why `NewClient` now rejects any API key
  that is not printable ASCII (0x21-0x7E). `Comparison` and `Plan` decode
  errors go through the same strip, redact and truncate pipeline as
  `HTTPError.Message`, and a `Retry-After` beyond the int range is capped at
  24 h instead of ignored.
- **`pkg/insurance`:** decode errors no longer echo an uncapped vendor value
  (#336). A rejected enum value is shown only when it is a short identifier
  (`[A-Za-z0-9_.-]`, up to 32 bytes); anything else is reported as
  `(N bytes, not shown)`, so a hostile response cannot flood logs or inject
  text into an error that a consumer prints or returns to an LLM.
- **Units change (Azure Consumption recommendations):** the cost fields were
  amounts over the API's lookback window but were treated as monthly figures
  and term totals (#267). For recommendations from the Consumption
  ReservationRecommendations API (compute, database, cache, cosmosdb,
  synapse, managedredis; the Advisor path is unchanged):
  `OnDemandCost` and `EstimatedSavings` are now monthly run-rates
  (window amount x 30.4375 / lookback days; savings is net of overage);
  `CommitmentCost` is now Count x the reservation retail price for the term
  (upfront variant) or 0 (monthly variant); `RecurringMonthlyCost` is now 0
  (upfront) or Count x price / term months (monthly), never the window total.
  The lookback is requested explicitly (`Last7Days`) and read from the
  response. A recommendation with a missing or unknown lookback or term, a
  non-USD modern currency, or no resolvable reservation price is skipped with
  an error log instead of being returned with guessed figures. A price lookup
  that fails to complete (transport error, HTTP error status, cancelled or
  expired context) instead fails the whole service's `GetRecommendations` call.
  Consumers that cache or compare these fields (caps, payback, savings
  thresholds) will see different magnitudes after upgrading. In this repo,
  `pkg/reporter/reporter.go` (lines 44, 96) and `pkg/common/audit.go` (line
  301) use `CommitmentCost` and will now see 0 for monthly variants; the
  platform's `recTotalCommitment` must add `RecurringMonthlyCost` x term
  months to get the true total commitment of a monthly variant. Those
  consumers are not changed here
- Remove debug console.log from frontend recommendation handler
- Align pre-commit gocyclo threshold (10) with CI pipeline
- Pin tool versions in GitHub Actions for reproducible builds
- Update README Go version badge to match go.mod (1.25+)
- The local git-secrets setup script aborted on its PEM pattern and, when
  patched past that, made every scan fail on an invalid regex; its keyword
  allowlist whitelisted whole lines containing common Go/Terraform tokens.
  Allowlisting now lives in `.gitallowed` as literal entries and the PEM
  detector covers PKCS#8 keys (#1972)

## [0.9.0] - 2026-03-06

### Added

- RI Exchange feature: reshape analysis with normalization factors, API
  endpoints, and frontend page for managing convertible Reserved Instances
- RI utilization tracking from Cost Explorer with pagination support
- Convertible RI listing in EC2 client
- Security headers on all Lambda responses
- Admin password resolution from cloud secret managers

### Fixed

- Harden RI exchange handlers with validation and error sanitization
- Fix async race conditions and input validation in RI exchange frontend
- Fix base64 encoding for saveProfile and resetPassword
- Remove duplicate logout event handler
- Guard DNS zone outputs against missing resources (GCP, Azure)
- Fix Azure CDN redirect type and SPA routing
- Add network policies and resource quotas to AKS module
- Add security headers to Azure Front Door and GCP load balancer
- Wire admin password secrets through all cloud environment root modules

## [0.8.0] - 2026-02-01

### Added

- Deployment health check blocks for AWS, Azure, and GCP Terraform modules
- GCP self-signed cert for dev HTTPS
- Azure Front Door API routing and custom domain support
- Cross-provider deployment test harness script
- Azure ACR resource and registry authentication

### Fixed

- Enforce SSL-only connections on GCP Cloud SQL
- Migrate GCP load balancer to EXTERNAL_MANAGED with SPA routing
- Fix Azure Container Apps config and CDN delivery rule names
- Fix GCP frontend build trigger and database password generation
- Expand frontend CSP connect-src for Azure and GCP API origins
- Fix Fargate EventBridge container name
- Capture migration exit code correctly in entrypoint.sh

### Changed

- Convert AWS database from Aurora Serverless v2 to standalone RDS
- Move GCP Secret Manager out of database module
- Replace Azure Container App Jobs with Logic Apps scheduled tasks
- Simplify Azure database module

## [0.7.0] - 2026-01-15

### Added

- Full Terraform infrastructure for AWS (Fargate, Lambda, CloudFront, RDS),
  Azure (Container Apps, AKS, Front Door, PostgreSQL), and GCP (Cloud Run,
  GKE, Cloud SQL) with CI-specific tfvars
- PostgreSQL database with connection pool, migrations, and secret resolvers
- Authentication service with RBAC and API key support
- REST API with rate limiting, CORS, and middleware stack
- Email service with SMTP sender and cloud credential resolution
- Analytics collector, purchase execution, and scheduled task runner
- Docker containerization with multi-stage builds and compose configs
- GitHub Actions CI/CD pipeline (lint, test, security scan, Docker build,
  Terraform validate, E2E tests, Infracost)
- Frontend web dashboard with TypeScript, webpack, Chart.js

### Fixed

- Sanitize user input in dashboard and recommendations (XSS prevention)
- Add connection pool limits and graceful shutdown to server
- Add nil checks across Azure service clients
- Enforce 12-char minimum password with complexity requirements
- Use hidden-source-map for production frontend builds
- Use rightmost X-Forwarded-For IP for client identification
- Add SHA256 checksum verification for migrate binary in Docker
- Tighten git-secrets patterns to reduce false positives

## [0.6.0] - 2025-11-01

### Added

- Database Savings Plans support and SP type filtering
- OSL-3.0 license and contributing guidelines

### Fixed

- RDS RI purchase failing on details assertion and invalid reservation ID
- OpenSearch RI resource type and offering lookup
- Deduplicate reservation ID sanitization into pkg/common

### Changed

- Refactor internal packages to providers (aws, azure, gcp)
- Add provider-specific mocking infrastructure and tests

## [0.5.0] - 2025-09-01

### Added

- Multi-cloud support (Azure experimental, GCP experimental)
- API-based RDS extended support detection
- Instance type validation system
- CSV reader for recommendation import
- Duplicate RI purchase prevention
- Account alias lookup
- Confirmation prompt and instance limit features

### Changed

- Replace global variables with Config struct pattern
- Improve rate limiting and test performance
- Refactor all purchase clients with enhanced error handling

## [0.4.0] - 2025-07-01

### Added

- Multi-service RI support: EC2, ElastiCache, MemoryDB, OpenSearch, Redshift
- Multi-service orchestration and CLI
- Comprehensive test coverage (80%+ across packages)

### Fixed

- CSV pricing calculations to use AWS-provided cost data

## [0.3.0] - 2025-05-01

### Added

- Initial CLI tool for RDS Reserved Instance purchasing
- Recommendations fetching from AWS Cost Explorer
- CSV output for analysis results
- Go module setup with AWS SDK v2
