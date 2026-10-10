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

- **Breaking (source):** `common.PurchaseResult` gained an `ExistingCommitment`
  field. Downstream code that builds it with unkeyed composite literals no
  longer compiles; keyed literals are unaffected. The field is true when a
  provider adopted a commitment that already existed for the same idempotency
  token (#211)

### Fixed

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
