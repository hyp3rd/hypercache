include .project-settings.env

GOLANGCI_LINT_VERSION ?= v2.12.2
BUF_VERSION ?= v1.69.0
GO_VERSION ?= 1.26.3
GCI_PREFIX ?= github.com/hyp3rd/hypercache
PROTO_ENABLED ?= true

GOFILES = $(shell find . -type f -name '*.go' -not -path "./pkg/api/*" -not -path "./vendor/*" -not -path "./.gocache/*" -not -path "./.git/*")

init:
	./setup-project.sh --module $(shell grep "^module " go.mod | awk '{print $$2}')
	$(MAKE) prepare-toolchain
	@if [ "$(PROTO_ENABLED)" = "true" ]; then $(MAKE) prepare-proto-tools; fi

test:
	RUN_INTEGRATION_TEST=yes go test -v -timeout 5m -cover ./...

test-race:
	RUN_INTEGRATION_TEST=yes go test -race -count=10 -shuffle=on -timeout=15m ./...

typecheck:
	@echo "Running go vet..."
	go vet ./...

build:
	@echo "Building..."
	go build -v ./...

start-dev-cluster: stop-dev-cluster
	@echo "building and lifting a new hypercache stack"
	@echo
	COMPOSE_BAKE=true docker compose -f docker-compose.cluster.yml up --build

stop-dev-cluster:
	@echo "Stopping any previously running stack"
	@echo
	docker compose -f docker-compose.cluster.yml down -v --rmi local --remove-orphans

# OIDC end-to-end example. The full stack — cache cluster +
# Keycloak + Monitor — is defined and orchestrated from the
# monitor repo's `examples/oidc/`. This target is a thin
# passthrough so cache-repo operators can boot the stack
# without leaving their working tree. The monitor repo must be
# cloned as a sibling: ../hypercache-monitor.
start-oidc:
	@if [ ! -f ../hypercache-monitor/examples/oidc/docker-compose.yml ]; then \
		echo "expected ../hypercache-monitor/examples/oidc/docker-compose.yml; clone the monitor repo as a sibling"; \
		exit 1; \
	fi
	$(MAKE) -C ../hypercache-monitor start-oidc

stop-oidc:
	@if [ ! -f ../hypercache-monitor/examples/oidc/docker-compose.yml ]; then exit 0; fi
	$(MAKE) -C ../hypercache-monitor stop-oidc

clean-oidc:
	@if [ ! -f ../hypercache-monitor/examples/oidc/docker-compose.yml ]; then exit 0; fi
	$(MAKE) -C ../hypercache-monitor clean-oidc

# test-cluster brings up the 5-node docker-compose cluster, waits for
# every node's /healthz to be 200, runs the cross-node smoke test
# (PUT/GET/DELETE asserted on every node), and tears the stack down —
# always, even on assertion failure — so a failing run leaves no
# stragglers. The shell-script's exit code is propagated so CI can
# fail the build on any regression of the bugs that escaped Phase D
# initial review (factory dropped options, seeds without IDs,
# json.RawMessage on non-owner GET).
test-cluster: stop-dev-cluster
	@echo "spinning up cluster + running cross-node smoke + resilience"
	@echo
	docker compose -f docker-compose.cluster.yml up --build -d
	@bash scripts/tests/wait-for-cluster.sh
	@rc=0; bash scripts/tests/10-test-cluster-api.sh || rc=$$?; \
		if [ $$rc -eq 0 ]; then \
			echo ""; echo "smoke ok — running resilience phase"; echo ""; \
			bash scripts/tests/20-test-cluster-resilience.sh || rc=$$?; \
		fi; \
		echo ""; echo "tearing down cluster (rc=$$rc)"; \
		docker compose -f docker-compose.cluster.yml down -v --rmi local --remove-orphans >/dev/null 2>&1 || true; \
		exit $$rc

# ci aggregates the gates required before declaring a task done (see AGENTS.md).
ci: lint typecheck test-race pre-commit sec build
	@echo "All CI gates passed."

# bench runs the benchmark tests in the benchmark subpackage of the tests package.
bench:
	cd tests/benchmark && go test -bench=. -benchmem -benchtime=4s ./... -timeout 30m

# bench-baseline captures the current benchmark output to bench-baseline.txt for benchstat comparison.
bench-baseline:
	cd tests/benchmark && go test -bench=. -benchmem -benchtime=4s -count=5 . -timeout 30m | tee ../../bench-baseline.txt

# run-example runs the example specified in the example variable with the optional arguments specified in the ARGS variable.
run-example:
	go run ./__examples/$(group)/*.go $(ARGS)

update-deps:
	go get -u -t ./... && go mod tidy -v && go mod verify

prepare-toolchain: prepare-base-tools

prepare-base-tools:
	$(call check_command_exists,docker) || (echo "Docker is missing, install it before starting to code." && exit 1)

	$(call check_command_exists,git) || (echo "git is not present on the system, install it before starting to code." && exit 1)

	$(call check_command_exists,go) || (echo "golang is not present on the system, download and install it at https://go.dev/dl" && exit 1)

	@echo "Installing gci...\n"
	$(call check_command_exists,gci) || go install github.com/daixiang0/gci@latest

	@echo "Installing gofumpt...\n"
	$(call check_command_exists,gofumpt) || go install mvdan.cc/gofumpt@latest

	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)...\n"
	$(call check_command_exists,golangci-lint) || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$(go env GOPATH)/bin" $(GOLANGCI_LINT_VERSION)

	@echo "Installing staticcheck...\n"
	$(call check_command_exists,staticcheck) || go install honnef.co/go/tools/cmd/staticcheck@latest

	@echo "Installing govulncheck...\n"
	$(call check_command_exists,govulncheck) || go install golang.org/x/vuln/cmd/govulncheck@latest

	@echo "Installing gosec...\n"
	$(call check_command_exists,gosec) || go install github.com/securego/gosec/v2/cmd/gosec@latest

	@echo "Checking if pre-commit is installed..."
	pre-commit --version >/dev/null 2>&1 || echo "pre-commit not found; skipping hook installation (optional)"
	@if command -v pre-commit >/dev/null 2>&1; then \
		echo "Initializing pre-commit..."; \
		pre-commit validate-config || pre-commit install && pre-commit install-hooks; \
		echo "Installing pre-commit hooks..."; \
		pre-commit install; \
		pre-commit install-hooks; \
	fi

update-toolchain:
	@echo "Updating buf to latest..."
	go install github.com/bufbuild/buf/cmd/buf@latest && echo "buf version: " && buf --version

	@echo "Updating protoc-gen-go..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

	@echo "Updating protoc-gen-go-grpc..."
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

	@echo "Updating protoc-gen-openapi..."
	go install github.com/google/gnostic/cmd/protoc-gen-openapi@latest

	@echo "Updating gci...\n"
	go install github.com/daixiang0/gci@latest

	@echo "Updating gofumpt...\n"
	go install mvdan.cc/gofumpt@latest

	@echo "Updating govulncheck...\n"
	go install golang.org/x/vuln/cmd/govulncheck@latest

	@echo "Updating gosec...\n"
	go install github.com/securego/gosec/v2/cmd/gosec@latest

	@echo "Updating staticcheck...\n"
	go install honnef.co/go/tools/cmd/staticcheck@latest


lint: prepare-toolchain
	@echo "Proto lint/format (if enabled and buf is installed)..."
	@if [ "$(PROTO_ENABLED)" = "true" ] && command -v buf >/dev/null 2>&1; then \
		buf lint; \
		buf format -w; \
	elif [ "$(PROTO_ENABLED)" = "true" ]; then \
		echo "buf not installed, skipping proto lint/format (run make prepare-proto-tools to enable)"; \
	else \
		echo "PROTO_ENABLED is not true; skipping proto lint/format"; \
	fi

	@echo "Running gci..."
	@for file in ${GOFILES}; do \
		gci write -s standard -s default -s blank -s dot -s "prefix($(GCI_PREFIX))" -s localmodule --skip-vendor --skip-generated $$file; \
	done

	@echo "\nRunning gofumpt..."
	gofumpt -l -w ${GOFILES}

	@echo "\nRunning staticcheck..."
	staticcheck ./...

	@echo "\nRunning golangci-lint $(GOLANGCI_LINT_VERSION)..."
	golangci-lint run -v --fix ./...

vet:
	@echo "Running go vet..."

	$(call check_command_exists,shadow) || go install golang.org/x/tools/go/analysis/passes/shadow/cmd/shadow@latest

	@for file in ${GOFILES}; do \
		go vet -vettool=$(shell which shadow) $$file; \
	done

sec:
	@echo "Running govulncheck..."
	govulncheck ./...

	@echo "\nRunning gosec..."
	gosec -exclude-generated -exclude-dir=__examples/size ./...

docs-build:
	PYENV_VERSION=mkdocs mkdocs build --strict

docs-publish: docs-build
	PYENV_VERSION=mkdocs mkdocs gh-deploy

docs-serve: docs-build
	PYENV_VERSION=mkdocs mkdocs serve

pre-commit:
	@if command -v pyenv >/dev/null 2>&1; then \
		eval "$$(pyenv init -)" && \
		pyenv activate pre-commit && \
		pre-commit run -a trailing-whitespace && \
		pre-commit run -a end-of-file-fixer && \
		pre-commit run -a markdownlint && \
		pre-commit run -a yamllint && \
		pre-commit run -a cspell && \
		pre-commit run -a cspell; \
	else \
		echo "pyenv command not found"; \
	fi

# help prints a list of available targets and their descriptions.
help:
	@echo "Available targets:"
	@echo
	@echo "Development commands:"
	@echo "  prepare-toolchain\t\tInstall required development tools (core tooling)"
	@echo "  update-toolchain\t\tUpdate all development tools to their latest versions"
	@echo
	@echo "Testing commands:"
	@echo "  test\t\t\t\tRun all tests in the project"
	@echo "  test-race\t\t\tRun tests with -race -count=10 -shuffle=on"
	@echo "  bench\t\t\t\tRun benchmarks in tests/benchmark"
	@echo "  bench-baseline\t\tCapture benchmark baseline to bench-baseline.txt"
	@echo
	@echo "Code quality commands:"
	@echo "  ci\t\t\t\tRun the full quality gate (lint typecheck test-race sec build)"
	@echo "  lint\t\t\t\tRun all linters (gci, gofumpt, staticcheck, golangci-lint)"
	@echo "  typecheck\t\t\tRun go vet"
	@echo "  build\t\t\t\tRun go build ./..."
	@echo "  vet\t\t\t\tRun go vet and shadow analysis"
	@echo "  sec\t\t\t\tRun security analysis (govulncheck, gosec)"
	@echo
	@echo "  update-deps\t\t\tUpdate all dependencies and tidy go.mod"
	@echo
	@echo
	@echo "Documentation commands:"
	@echo "  docs-build"
	@echo "  docs-publish"
	@echo "  docs-serve"
	@echo
	@echo "For more information, see the project README."

.PHONY: init prepare-toolchain prepare-base-tools update-toolchain test test-race typecheck build ci bench bench-baseline vet update-deps lint sec help \
	start-dev-cluster stop-dev-cluster start-oidc stop-oidc clean-oidc
