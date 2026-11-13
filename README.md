# cloud-commitments-go

[![CI](https://github.com/LeanerCloud/cloud-commitments-go/actions/workflows/ci.yml/badge.svg)](https://github.com/LeanerCloud/cloud-commitments-go/actions/workflows/ci.yml)

Shared Go libraries for finding and buying cloud commitments (Reserved Instances, Savings Plans and committed use discounts) on AWS, Azure and GCP. The packages hold the provider integrations, the recommendation types and the scoring and filtering logic that the [CUDly CLI](https://github.com/LeanerCloud/cloud-commitments-cli), the [MCP server](https://github.com/LeanerCloud/cloud-commitments-mcp) and the [self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform) all build on.

This repository is a set of libraries, so there's no binary to install. Purchase confirmation, dry-run policy and other safety controls live in the callers, and any caller that executes a purchase can spend money.

## What's in it

The repository has five Go modules and no root module, so run Go commands from inside a module directory.

| Module | Contents |
|---|---|
| [`pkg`](pkg) | Provider interfaces and shared domain packages: [`common`](pkg/common) types and JSON codecs, [`provider`](pkg/provider) credential detection and discovery, [`scorer`](pkg/scorer) and [`recfilter`](pkg/recfilter) for ranking and filtering recommendations, [`reccontract`](pkg/reccontract) for the recommendation request contract, [`exchange`](pkg/exchange) for AWS Convertible RI exchanges, [`ladder`](pkg/ladder) for commitment-laddering plans, [`retry`](pkg/retry), [`httpclient`](pkg/httpclient), [`concurrency`](pkg/concurrency), [`logging`](pkg/logging) and [`reporter`](pkg/reporter) |
| [`providers/aws`](providers/aws) | AWS integration with service clients for EC2, RDS, ElastiCache, MemoryDB, OpenSearch, Redshift and Savings Plans |
| [`providers/azure`](providers/azure) | Azure integration, including Advisor-based recommendations across subscriptions |
| [`providers/gcp`](providers/gcp) | GCP integration |
| [`ci_cd_sanity_tests`](ci_cd_sanity_tests) | Diagnostic checks that call real cloud APIs, used by the sanity workflows and not meant for end users |

Provider support varies by service and account. RDS and ElastiCache on AWS are the tested paths, and the other AWS services, Azure and GCP are experimental.

## Use it in a Go project

There are no tags or releases yet, so consumers pin a commit. For example:

```bash
go get github.com/LeanerCloud/cloud-commitments-go/pkg@main
go get github.com/LeanerCloud/cloud-commitments-go/providers/aws@main
```

Each module requires the Go version declared in its `go.mod` (currently 1.26.11). The CLI shows the pattern in its own `go.mod`, which pins all four library modules to the same pseudo-version.

## Build and test

```bash
make build
make test-unit
```

Both targets loop over every module. Unit tests run with `-race -short` and need no cloud credentials. To check one module the way an external consumer sees it, without the workspace:

```bash
(cd pkg && GOWORK=off go test -race -short ./...)
```

Don't run the binaries under `ci_cd_sanity_tests/cmd/` unless you mean to hit the account your credentials point at. The full pre-merge set is `make ci`, which runs format, vet, complexity, unit tests and a security scan.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) first, and report security problems as described in [SECURITY.md](SECURITY.md). Design notes are under [docs/design](docs/design), and [CHANGELOG.md](CHANGELOG.md) tracks changes.

## License

CUDly is maintained by [LeanerCloud](https://github.com/LeanerCloud) and licensed under the [Open Software License 3.0](LICENSE). Third-party notices are in [NOTICE](NOTICE).
