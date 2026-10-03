# Show available development commands.
default:
    @just --list

# Prepare the pinned development tools and Go dependencies after cloning.
setup:
    mise trust mise.toml
    mise install
    mise exec -- go mod download

# Format Go sources in place with pinned gofumpt.
fmt:
    golangci-lint fmt ./...

# Reject non-gofumpt formatting without changing sources; print a diff.
fmt-check:
    golangci-lint fmt --diff ./...

# Run Go's static checks.
vet:
    go vet ./...

# Run pinned linters and format checks without changing source or module files.
lint:
    golangci-lint run ./...

# Apply supported linter fixes; source files may change.
lint-fix:
    golangci-lint run --fix ./...

# Run all project tests.
test:
    go test ./...

# Check module files without rewriting go.mod or go.sum.
mod-check:
    go mod tidy -diff

# Build a local binary without installing it.
build:
    go build .

# Build a local release binary without debugger symbols or DWARF information.
build-release:
    go build -ldflags="-s -w" .

# Run the CLI, forwarding arguments.
run *args:
    go run . {{args}}

# Run the checks required before committing; never rewrite source files.
precommit: lint test build
