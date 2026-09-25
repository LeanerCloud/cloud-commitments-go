# cloud-commitments-go shared Go libraries

This repository contains the shared provider interfaces, domain packages, and
cloud-provider integrations used by the CLI, MCP server, and self-hosted
platform. It is not an application CLI and does not produce a CUDly binary.

The intended public repository is `LeanerCloud/cloud-commitments-go`; it is not
published yet. The committed `go.work` is sufficient for development in this
checkout. Consumer repositories may still require unpublished shared-module
versions until the publication sequence completes.

## Modules

The repository has five Go modules:

- [`pkg`](pkg) contains shared domain and utility packages, including [`pkg/common`](pkg/common) and [`pkg/provider`](pkg/provider).
- [`providers/aws`](providers/aws), [`providers/azure`](providers/azure), and [`providers/gcp`](providers/gcp) contain provider integrations.
- [`ci_cd_sanity_tests`](ci_cd_sanity_tests) contains diagnostic CI and integration checks. It is not an end-user CLI.

There is no root Go module. Use the module directories above when you inspect, build, or test this component.

## Build and test

Use the Go version declared in each module's `go.mod` (currently Go 1.26.6).

```bash
make build
make test-unit
```

`make build` and `make test-unit` iterate over the five module directories. These targets do not create a shared library binary. Unit tests do not require cloud credentials. The diagnostic binaries in `ci_cd_sanity_tests/cmd/` are explicitly cloud-backed checks and must not be run unless the target account and credentials are intended.

For standalone module verification in this checkout, use the module-local
command with `GOWORK=off`, for example:

```bash
(cd pkg && GOWORK=off go test -race -short ./...)
```

The provider modules' committed relative replacements resolve the local `pkg`
module during this check. After publication, repeat the verification against
released module versions with `GOWORK=off` and no local replacement; that is
the external-consumer release check.

## Provider boundaries

Provider support varies by service and account. Amazon RDS and ElastiCache are the tested AWS service paths. Other AWS service paths, Azure, and GCP support remains experimental.

These packages expose provider operations to callers. Purchase confirmation, dry-run policy, and user-facing safety controls belong to the CLI, MCP server, or platform caller. Any caller that executes a purchase can spend money.

Caller-specific behavior is documented in the CLI, MCP server, and self-hosted
platform components. Those destination repositories are intended but not yet
published; do not treat this library as providing their purchase controls.

## License and attribution

CUDly is maintained by [LeanerCloud](https://github.com/LeanerCloud) and licensed under the [Open Software License 3.0](LICENSE). See the repository license and attribution files for third-party notices.
