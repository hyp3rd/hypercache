include .project-settings.env

GOLANGCI_LINT_VERSION ?= v2.7.2
BUF_VERSION ?= v1.61.0
GO_VERSION ?= 1.25.6
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
	go test -race ./...

# bench runs the benchmark tests in the benchmark subpackage of the tests package.
bench:
	cd tests/benchmark && go test -bench=. -benchmem -benchtime=4s . -timeout 30m

# run-example runs the example specified in the example variable with the optional arguments specified in the ARGS variable.
run-example:
	go run ./__examples/$(group)/*.go $(ARGS)

update-deps:
	go get -v -u ./...
	go mod tidy

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

# check_command_exists is a helper function that checks if a command exists.
define check_command_exists
@which $(1) > /dev/null 2>&1 || (echo "$(1) command not found" && exit 1)
endef

ifeq ($(call check_command_exists,$(1)),false)
  $(error "$(1) command not found")
endif

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
	@echo
	@echo "Code quality commands:"
	@echo "  lint\t\t\t\tRun all linters (gci, gofumpt, staticcheck, golangci-lint)"
	@echo "  vet\t\t\t\tRun go vet and shadow analysis"
	@echo "  sec\t\t\t\tRun security analysis (govulncheck, gosec)"
	@echo
	@echo "  update-deps\t\t\tUpdate all dependencies and tidy go.mod"
	@echo
	@echo
	@echo "For more information, see the project README."
.PHONY: init prepare-toolchain prepare-base-tools update-toolchain test bench vet update-deps lint sec help
