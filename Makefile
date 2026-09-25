.PHONY: all build clean test test-unit test-integration test-coverage full-test fmt vet lint \
	complexity complexity-report security-scan security-scan-go security-scan-snyk \
	security-scan-all pre-commit setup-git-secrets install-dev-tools help ci tidy-check

MODULES := pkg providers/aws providers/azure providers/gcp ci_cd_sanity_tests
GOLANGCI_LINT_VERSION?=v2.10.1
GOSEC_VERSION?=v2.28.0
GOCYCLO_VERSION?=v0.6.0
STATICCHECK_VERSION?=v0.7.0

all: build

help:
	@echo "Available targets:"
	@echo "  build              - Build all Go modules"
	@echo "  test-unit          - Run unit tests in all modules"
	@echo "  test-integration   - Run integration tests"
	@echo "  test-coverage      - Run tests with per-module coverage reports"
	@echo "  clean              - Remove Go build artifacts"
	@echo "  fmt                - Format Go code in all modules"
	@echo "  lint               - Run golangci-lint in all modules"
	@echo "  complexity         - Check cyclomatic complexity in all modules"
	@echo "  security-scan      - Run Go security scanners in all modules"
	@echo "  security-scan-snyk - Run Snyk in all modules"
	@echo "  ci                 - Run CI pipeline locally"

build:
	@for mod in $(MODULES); do \
		echo "Building $$mod..."; \
		(cd "$$mod" && go build ./...) || exit 1; \
	done

test: test-unit

test-unit:
	@for mod in $(MODULES); do \
		echo "Testing $$mod..."; \
		(cd "$$mod" && go test -v -race -short ./...) || exit 1; \
	done

test-integration:
	@for mod in $(MODULES); do \
		echo "Running integration tests in $$mod..."; \
		(cd "$$mod" && go test -v -race -tags=integration ./...) || exit 1; \
	done

test-coverage:
	@for mod in $(MODULES); do \
		echo "Generating coverage for $$mod..."; \
		(cd "$$mod" && go test -v -race -coverprofile=coverage.out -covermode=atomic ./... && go tool cover -html=coverage.out -o coverage.html && go tool cover -func=coverage.out) || exit 1; \
	done

full-test: test-unit test-integration test-coverage

clean:
	@for mod in $(MODULES); do \
		(cd "$$mod" && rm -f coverage.out coverage.html gosec-report.json complexity-report.txt && go clean) || exit 1; \
	done

fmt:
	@for mod in $(MODULES); do \
		echo "Formatting $$mod..."; \
		(cd "$$mod" && go fmt ./...) || exit 1; \
	done

tidy-check:
	@version=$$(awk '/^[[:space:]]*go([[:space:]]|$$)/ { if (NF != 2) { print "__malformed__"; next } print $$2 }' pkg/go.mod); \
	count=$$(printf '%s\n' "$$version" | awk 'NF { n++ } END { print n + 0 }'); \
	if [ "$$count" -ne 1 ] || ! printf '%s\n' "$$version" | awk '$$0 !~ /^[0-9]+\.[0-9]+\.[0-9]+$$/ { exit 1 }'; then \
		echo "expected exactly one patch-level Go version in pkg/go.mod" >&2; exit 1; \
	fi; \
	for mod in $(MODULES); do \
		echo "Checking go mod tidy in $$mod..."; \
		(cd "$$mod" && GOTOOLCHAIN="go$$version" GOWORK=off go mod tidy -diff) || { echo "go mod tidy check failed for module $$mod" >&2; exit 1; }; \
	done

vet:
	@for mod in $(MODULES); do \
		echo "Vetting $$mod..."; \
		(cd "$$mod" && go vet ./...) || exit 1; \
	done

lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed. Install: make install-dev-tools" >&2; exit 1; }
	@for mod in $(MODULES); do \
		echo "Linting $$mod..."; \
		(cd "$$mod" && golangci-lint run --timeout=5m) || exit 1; \
	done

complexity:
	@command -v gocyclo >/dev/null || { echo "gocyclo not installed. Install: make install-dev-tools" >&2; exit 1; }
	@for mod in $(MODULES); do \
		echo "Checking complexity in $$mod..."; \
		(cd "$$mod" || exit 1; if ! issues="$$(gocyclo -over 10 -ignore '.*_test\.go' .)"; then echo "gocyclo failed" >&2; exit 1; fi; if [ -n "$$issues" ]; then echo "$$issues" >&2; exit 1; fi) || exit 1; \
	done

complexity-report:
	@command -v gocyclo >/dev/null || { echo "gocyclo not installed. Install: make install-dev-tools" >&2; exit 1; }
	@for mod in $(MODULES); do \
		echo "Writing complexity report for $$mod..."; \
		(cd "$$mod" && gocyclo -top 20 -ignore '.*_test\.go' . > complexity-report.txt && cat complexity-report.txt) || exit 1; \
	done

security-scan: security-scan-go

security-scan-go:
	@command -v gosec >/dev/null || { echo "gosec not installed. Install: make install-dev-tools" >&2; exit 1; }
	@for mod in $(MODULES); do \
		echo "Scanning $$mod..."; \
		(cd "$$mod" && gosec -fmt=json -out=gosec-report.json -exclude=G101,G104,G115,G204,G301,G304,G402,G505 ./...) || exit 1; \
	done

security-scan-snyk:
	@command -v snyk >/dev/null || { echo "snyk not installed. Install: npm install -g snyk" >&2; exit 1; }
	@for mod in $(MODULES); do \
		echo "Scanning $$mod with Snyk..."; \
		(cd "$$mod" && snyk test --severity-threshold=high) || exit 1; \
	done

security-scan-all: security-scan security-scan-snyk

ci: fmt vet complexity test-unit security-scan

pre-commit: fmt vet complexity test-unit

setup-git-secrets:
	@echo "Setting up git-secrets..."
	@bash scripts/setup-git-secrets.sh

install-dev-tools:
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@echo "Installing gosec $(GOSEC_VERSION)..."
	@go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	@echo "Installing staticcheck $(STATICCHECK_VERSION)..."
	@go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	@echo "Installing gocyclo $(GOCYCLO_VERSION)..."
	@go install github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION)
