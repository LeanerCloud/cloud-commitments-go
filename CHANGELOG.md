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

- **`ladder.AllocateResult.NetExistingUSDPerHour`:** the net existing
  commitment (existing minus expiring, summed over layers) that `Allocate`
  sized the gap against, set on every successful result including the
  hold-only exits. Callers can report it instead of recomputing a gross
  figure from the layer states, so a plan's existing figure reconciles with
  its gap. `Allocate` now checks the snapshot before the baseline-unavailable
  hold, so expiring above existing is an error there too (#83).
- **`exchange.WithOfferedInstanceTypes` / `RunAutoExchangeParams.OfferedInstanceTypes`:**
  restrict RI reshape targets to the instance types the region offers.
  `AnalyzeReshaping` and `AnalyzeReshapingWithRecs` take it as a variadic
  `ReshapeOption`; an empty list keeps the previous size ladder, and a family
  missing from a non-empty list gets a recommendation with the reason
  "instance family not offered in region". Auto exchange now reports the
  recommendation's own reason for every skipped RI without a target (#563).
- **`RecommendationParams.RIAccountScope`:** selects the Cost Explorer account
  scope (`linked` default, or `payer`) for Reserved Instance recommendations,
  mirroring `SPAccountScope`. Empty keeps the previous linked behaviour; an
  unknown value is rejected (#508).
- **`recommendations.RederiveFamilyCoverage`:** recomputes the family-wide
  RDS `ProjectedCoverage` that family-NU sizing wrote, after a caller changed
  `Count` (`--max-instances`, count override, duplicate-RI dedupe). It shares
  its formula with sizing, returns a new slice and leaves non-RDS, SQL Server
  and unsized recs untouched (#511).
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

- **`aws/recommendations`:** the four Cost Explorer coverage and utilization page fetchers share one generic `fetchWithRetry` helper (rate limiter plus concurrency cap); behaviour and error messages are unchanged (#120).
- **`aws/rds`:** the RDS purchase path uses the shared `purchaseguard` idempotency guard and AlreadyExists recovery, like ElastiCache. Behavior for valid purchases is unchanged (#10).
- **`aws/memorydb`:** the MemoryDB purchase path uses the shared `purchaseguard` idempotency guard and AlreadyExists recovery, like ElastiCache. Behavior for valid purchases is unchanged; only the lookup-failure error wording changes to "before MemoryDB reservation purchase" (#10).
- **`aws/opensearch`:** the OpenSearch purchase path uses the shared `purchaseguard` idempotency guard and AlreadyExists recovery, like ElastiCache. Behavior for valid purchases is unchanged (#10).
- **`azure/cache`:** `GetValidResourceTypes` now returns an error when the Redis cache list reaches the page cap (#249) instead of logging a warning and returning a partial SKU set.
- **`scripts/setup-git-secrets.sh`:** the database DSN detector now fires on `user:password@host` (the old pattern ended at `@`, which `git-secrets`' word-match scan rejected before a hostname), and also covers `postgresql`, `mariadb`, driver-suffixed schemes and empty usernames (#125).
- **`provider`:** `ProviderConfig` is renamed to `Config` (#39). `ProviderConfig` stays as a type alias marked `Deprecated: use Config.`, so existing consumers keep compiling, but consumers that run staticcheck with fatal warnings (cloud-commitments-mcp, ~86 uses, and cloud-commitments-platform, ~36 uses) fail lint with SA1019 on their next `pkg` bump until they migrate to `provider.Config`. The `aws`, `azure` and `gcp` provider modules and `ci_cd_sanity_tests` already use `provider.Config`.
- **`httpclient`:** dials to the RFC 8215 local-use NAT64 prefix `64:ff9b:1::/48` are refused when any RFC 6052 layout (/48, /56, /64, /96) embeds a metadata IPv4 address; Teredo `2001::/32` stays allowed (#238).
- **CI:** the security-scan job runs a pinned gitleaks CLI (v8.30.1, sha256-verified) over the checkout with `.gitleaks.toml` and fails on any finding (#101).
- **`common.HoursPerMonth`:** exported 730-hour average month (`HoursPerYear / 12`) next to `HoursPerYear`; the remaining bare 730 and 8760 literals in the aws and gcp providers use the shared constants (#109).
- **`gcp`:** region-lookup permission errors are now classified by type only (googleapi 403 or gRPC `PermissionDenied`) through the shared `IsPermissionError`. gRPC `PermissionDenied` is now skipped with a warning like a 403, and an untyped error whose text merely contains "403" and "permission" now propagates instead of being skipped (#6).
- **BREAKING `gcp`: stored contexts removed (#6).** `computeengine.NewClient`
  no longer takes a `context.Context` (it was stored and never read), and
  `computeengine.BillingService.ListSKUs` now takes `ctx` as its first
  parameter, so custom implementations must add it. `Provider` and
  `RecommendationsClientAdapter` no longer store a context (unexported
  fields; no API change). Downstream call sites to update: mcp
  `tools/gcp_computeengine_cud_safeguards_test.go:76,125`,
  `tools/search_dedupe_test.go:344`,
  `cmd/cudly-mcp/gcp_purchase_protocol_test.go:102`; platform
  `internal/purchase/gcp_history_cost_test.go:48`. No cli, mcp or platform
  code references `BillingService`, `ListSKUs` or a `gcp.Provider` literal.
- **`azure`:** managedredis and synapse reservations now get the same generated display name as the other services (`redis-<region>-<sku>-<n>x-<term>-<payment>-...` and `synapse-...`) instead of the fixed `Azure Cache for Redis Reservation - <sku>` / `Synapse SQL Pool Reservation - <sku>`. This also removes the stray space before the idempotency marker on managedredis. Idempotency lookup keys on the tag or the marker suffix, not the base name (#538).
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

- **`azure`:** recommendations from the Consumption path (compute, database, cache, cosmosdb, managedredis, synapse) and the Savings Plan benefit path now set `CommitmentCurrency` to `USD`, because their commitment amounts come from sources already required to be USD. The Advisor and legacy paths carry no checked commitment amounts and stay empty (#623).
- **AWS/OpenSearch:** the STS account-ID lookup used for post-purchase tagging caches only a successful result, so one throttled or timed-out `GetCallerIdentity` no longer disables tagging for the client's lifetime (#53).
- **AWS/recommendations:** `normalizeRegionName` maps the current `Europe (Ireland|Frankfurt|London|Paris|Stockholm)` Cost Explorer display names to region codes, so coverage lookups keyed by region code match those recommendations (#53).
- **AWS/recommendations:** `normalizeRegionName` also maps `Asia Pacific (Malaysia|Thailand)`, `Canada West (Calgary)` and `Mexico (Central)` to `ap-southeast-5`, `ap-southeast-7`, `ca-west-1` and `mx-central-1` (#53).
- **AWS/recommendations:** `normalizeRegionName` also maps `Asia Pacific (Taipei)` (`ap-east-2`), `Asia Pacific (New Zealand)` (`ap-southeast-6`) and the China, GovCloud, ISO and European Sovereign Cloud region descriptions from the SDK partitions file (#53). The alias table is now a package-level `regionAliases` map, the no-op region-code prefix branch is removed (output unchanged for every input), and the collision test covers the whole map.
- **AWS/Savings Plans:** `GetExistingCommitments` returns an error naming the plan ID and value when a plan has an unparseable start or end date, instead of silently leaving the date zero (#53).
- **AWS/Savings Plans:** `GetExistingCommitments` returns an error naming the plan ID when a plan has no start or end date; a zero start date was never treated as recent by the purchase dedupe, so a duplicate purchase could slip through (#53).
- **AWS/RDS:** `PurchaseCommitment` refuses an offering whose upfront (`FixedPrice` x count) exceeds the approved recommendation upfront by more than 5%, or that is priced in a non-USD currency; a zero approved upfront is not checked (#53).
- Bump Go to 1.26.10 to pick up the fix for GO-2026-6809 (crypto/x509), which `govulncheck` now reports against 1.26.9.
- **AWS/MemoryDB, OpenSearch, Redshift:** `PurchaseCommitment` refuses an offering whose upfront (`FixedPrice` x count) exceeds the approved recommendation upfront by more than 5%, or that is priced in a non-USD currency (MemoryDB offerings carry no currency, so only the recommendation's currency is checked there), or, for MemoryDB, an upfront offering with a zero price or one bought in a China region (cn-north-1, cn-northwest-1), where the assumed USD price is wrong; a zero approved upfront is not checked. All AWS RI price guards (EC2, RDS, MemoryDB, OpenSearch, Redshift) also refuse a NaN or infinite approved upfront (#53).
- AWS ElastiCache purchases refuse a recommendation with no engine instead of
  sending an empty `ProductDescription` filter, send the engine lowercased as
  ElastiCache reports it, and verify the chosen offering's node type, duration
  and engine before buying it (#53).
- AWS EC2 RI purchases refuse an offering whose total upfront exceeds the
  recommendation's approved upfront by more than 5% (USD only; a
  recommendation without an upfront figure is not checked), so a repriced or
  differently-priced offering can no longer bypass the approved cap (#53).
- **`ReshapeBuffer` rejects nil payment caps** instead of mapping them to
  `math.MaxFloat64`: `MaxPaymentPerExchangeUSD` and `MaxPaymentDailyUSD`
  are now required, so an omitted cap can no longer mean unlimited spend on
  the auto-exchange path (#83).
- **`aws/recommendations`:** `GetRecommendations` for RI services now rejects an unrecognized term, payment option or lookback period instead of letting Cost Explorer apply its own defaults under the requested label; the deprecated silent converters are removed (#53).
- **`aws/recommendations`:** `GetRecommendations` for RI services now rejects a non-empty unrecognized term, payment option or lookback period instead of letting Cost Explorer apply its own defaults under the requested label; an unset (empty) value still means the Cost Explorer default. The deprecated silent converters are removed (#53).
- **Azure savings plan recommendations use a 730 hour month** (was 730.5),
  matching `common.HoursPerMonth` and the 8760 hour year; monthly recurring
  cost, on-demand cost and savings shift by about 0.07% (#583).

- **`exchange.AnalyzeReshaping`:** no longer proposes a same-capacity reshape
  (1x m5.24xlarge to 2x m5.12xlarge); it frees no units and AWS needs an
  equal-or-greater exchange value, so such RIs now get no recommendation and
  no quote (#563).
- **`common.EC2NormalizationFactor`:** knows the 32xlarge, 48xlarge, 56xlarge,
  96xlarge and 112xlarge sizes (256 to 896), so RI exchange analysis no longer
  skips RIs of those sizes with "No normalization factor known" (#563).
- **exchange:** `RunAutoExchange` now stops and returns the error when cancelling stale pending exchanges fails, instead of logging it and creating new exchanges while a stale pending one may still be approvable.
- **exchange:** `RunAutoExchange` now rejects any mode other than `auto` or `manual` (including empty) with an error before any store or exchange call; previously every non-`manual` value auto-executed exchanges.
- **common:** `BuildReservationName` no longer drops its random uniqueness suffix when a long SKU overflows the 60-char cap (it dropped it first, so long-SKU purchases got deterministic, colliding names). The timestamp and payment segments drop first, then the SKU is truncated; the suffix is always kept (#123).
- **logging:** `SetOutput` no longer races with `Logger.With` and concurrent log calls: the unsynchronized mirror of the writer is gone and the destination lives only in the mutex-guarded `log.Logger` (#123).
- **common:** `DecodeServiceDetailsFor` now rejects a non-empty details payload for a service with no typed details instead of silently returning nil details; MemoryDB (whose recommendations carry `CacheDetails`) now decodes as `CacheDetails` (#123).
- **common:** `DecodeServiceDetailsFor` now rejects a non-empty details payload for a service with no typed details instead of silently returning nil details; MemoryDB (whose recommendations carry `CacheDetails`) and NoSQL (Azure Cosmos DB, `NoSQLDetails`) now decode to their typed details, so MemoryDB rows without a payload decode to a zero `*CacheDetails` instead of nil (#123).
- **common:** `DecodeServiceDetailsFor` now rejects a non-empty details payload for a service with no typed details instead of silently returning nil details; MemoryDB (whose recommendations carry `CacheDetails`) and NoSQL (Azure Cosmos DB, `NoSQLDetails`) now decode to their typed details, so MemoryDB rows without a payload decode to a zero `*CacheDetails` instead of nil. The rejection wraps `ErrUntypedServiceDetails` for `errors.Is` (#123).
- **retry:** jittered backoff is now clamped to `MaxDelay` after jitter is applied (upward jitter could exceed it), and the `Do` / `PerAttemptTimeout` docs now say the per-attempt context is derived from, and cancelled with, the outer context (#123).
- **aws/ec2:** `GetOfferingDetails` always priced the convertible offering and its doc comment wrongly said the class does not change prices. Added `GetOfferingDetailsForClass` to price the class the purchase uses; `GetOfferingDetails` keeps the convertible default (#570).
- **A Multi-AZ size-flexible RDS reservation now covers Single-AZ demand in the
  duplicate-purchase guard** (#323): it counts as twice the size units of its
  family, so a recent Multi-AZ db.r6g.large PostgreSQL reservation suppresses
  two Single-AZ db.r6g.large recommendations. Single-AZ never covers Multi-AZ,
  and exact-class engines such as SQL Server keep their key.
- **Size-flexible RDS reservations now cover Single-AZ and Multi-AZ demand in
  the duplicate-purchase guard** (#323): Single-AZ and Multi-AZ share one
  family budget in size units, and a Multi-AZ node costs twice the units. A
  recent Multi-AZ db.r6g.large PostgreSQL reservation suppresses two Single-AZ
  db.r6g.large recommendations, and a Single-AZ db.r6g.xlarge suppresses one
  Multi-AZ db.r6g.large. Exact-class engines such as SQL Server keep their key.
- **The duplicate-purchase guard now treats regional Linux/Unix default-tenancy
  EC2 reservations as size-flexible** (#323): a recent m5.xlarge reservation
  suppresses two m5.large recommendations in the same region, counted in
  normalized units. Zonal, Windows/RHEL/SUSE, dedicated, wildcard and
  unknown-size entries stay exact-type, as do the families AWS excludes from
  size flexibility (G4ad, G4dn, G5, G5g, G6, G6e, G6f, Gr6, Gr6f, hpc7a, P5,
  Inf1, Inf2, u7i-6tb, u7i-8tb). A partly spent reservation is never credited
  as a whole node.
- **Unbounded AWS pagination loops now stop at a page cap and honour
  cancellation** (#561): the existing-commitment and valid-resource-type scans
  for ElastiCache, RDS, OpenSearch, Redshift and EC2 check the context before
  each page and fail with an error after 50 pages instead of looping forever.
  The Savings Plans existing-commitment scan now errors at its page cap instead
  of returning a partial list to the duplicate-purchase guards.
- **Duplicate guard counts an existing RDS reservation against other sizes of
  the same family** (#323): an AWS RDS reservation now pays for recommendations
  of any size in the same instance family, engine, region and deployment, by
  normalized units, instead of matching only the exact instance class. Only
  MySQL, MariaDB, PostgreSQL and Aurora are size-flexible; SQL Server, Oracle
  (until both paths carry the license model, #342), Db2 and unknown engines
  still match the exact class.

- **`aws/savingsplans`:** Savings Plans offering lookup, validation and purchase now fail with `ErrUnsupportedPartition` before any AWS call for `cn-*` (aws-cn) regions, whose plans are priced in CNY while the purchase path is USD-only; `aws` and `aws-us-gov` are unchanged (#7).
- **MemoryDB reserved-node and offering listings are page-capped and
  cancellable** (#556): `GetExistingCommitments` and `GetValidResourceTypes`
  now check the context per page and fail loudly after 50 pages instead of
  looping on a NextToken that never ends.
- **`exchange`:** reshape no longer treats `metal` as a fixed 192-unit size: it is removed from the size table, so `NormalizationFactorForSize("metal")` returns 0 (unknown), no recommendation targets a `*.metal` type, and a metal RI without a caller-supplied `NormalizationFactor` is reported as skipped with a reason instead of being sized as 192 units or dropped silently (#552).
- **`exchange`:** reshape recommendations no longer propose a target with more normalized capacity than the source RI owns (3x `m5.xlarge` at 80% used to become 2x `m5.2xlarge`, 32 units against 24 owned); `findBestFit` now steps down to a smaller size when rounding up would exceed the owned units, and skips the RI when nothing fits (#551).
- **`azure`:** compute `PurchaseCommitment` now checks the term before the payment option and returns the term error unwrapped (no `failed to marshal request:` prefix), like the other six services (#538).
- **`azure`:** synapse's zero-quantity purchase error now includes the quantity (`..., got 0`), like the other six services (#538).
- **`azure`:** `PurchaseCommitment` for cache, compute, cosmosdb, database, managedredis and search now rejects a blank resource type before any HTTP call, as synapse already did, instead of sending an empty `sku.name` (#538).
- **`azure`:** The shared `CUDLY_MAX_PARALLELISM` slot is now held per outbound call (page fetch, pricing page, SKU or capability request) instead of across a whole per-service sweep, so a long Azure sweep no longer starves AWS and GCP calls (#8).
- **`scorer`:** `MinCount` no longer applies to Savings Plans. Their `Count`
  is always 1 (the commitment is dollar-denominated), so `--min-count 2`
  dropped every Savings Plan recommendation.
- **`aws/recommendations`:** RDS family-NU sizing and the expiry deduction no longer pool SQL Server by instance family (its RIs are never size-flexible): those recs go to the per-size path and expiring SQL Server RIs count as `Unusable`. Oracle stays pooled until the license model is carried (#342).
- **`aws/recommendations`:** `FamilyExpiryReport.ExpiringNU` now reports the NU actually removed from family covered NU (before minus after) instead of the expiring RIs' own NU, so it is 0 when underutilized RIs absorb the expiry (#342).
- **`aws` and `azure/savingsplans` recommendations:** a Cost Explorer RI or
  Savings Plan detail with an explicit non-USD `CurrencyCode`, and an Azure
  benefit recommendation whose `currencyCode` is not USD, are skipped instead
  of passing non-USD `OnDemandCost`/`EstimatedSavings` through as USD. AWS
  reports each skip in `IncompleteRecommendationsError` (wraps
  `recommendations.ErrNotUSD`); Azure records it in the collection's skip log,
  surfacing as `PartialResultError`. An absent AWS `CurrencyCode` is unchanged
  (#124)

- **`aws/recommendations`:** the Savings Plans coverage and utilization and
  on-demand Cost Explorer fetchers now hold the shared concurrency slot around
  each call, so they count toward `CUDLY_MAX_PARALLELISM` (#8).
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

### Removed

- **BREAKING:** `common.Matches` (`pkg/common`) is removed. It compared only
  Provider, Region, Service, ResourceType and engine and had no callers in
  this repo, the CLI, the MCP server or the platform; use `pkg/recfilter`
  for dedupe. (#123)
- **BREAKING:** the `pkg/errors` package is removed (no importers). Removed
  exported symbols: `NotFoundError`, `ValidationError`, `AuthenticationError`,
  `AuthorizationError`, `ConflictError`, `RateLimitError`, `ServiceError`,
  their `New*`/`Is*` helpers (`NewNotFoundError`, `NewNotFoundErrorWithMessage`,
  `IsNotFoundError`, `NewValidationError`, `IsValidationError`,
  `NewAuthenticationError`, `IsAuthenticationError`, `NewAuthorizationError`,
  `IsAuthorizationError`, `NewConflictError`, `IsConflictError`,
  `NewRateLimitError`, `IsRateLimitError`, `NewServiceError`, `IsServiceError`)
  and the sentinels `ErrNotFound`, `ErrUnauthorized`, `ErrForbidden`,
  `ErrInvalidInput`, `ErrConflict`, `ErrInternalError`. (#123)
- **BREAKING:** the `pkg/config` package is removed (no importers). Removed
  exported symbols: `Load`, `DefaultScorerConfig`, `Config`, `AWSConfig`,
  `AzureConfig`, `GCPConfig` and `ServerConfig`. The `pkg` module no longer
  depends on `github.com/spf13/pflag`. (#123)

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
