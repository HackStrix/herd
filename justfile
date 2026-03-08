# Default recipe
default: test-race

# Run standard tests
test:
	go test ./...

# Run tests with race detector
test-race:
	go test -race ./...

# Run golangci-lint
lint:
	golangci-lint run ./...

# Tidy module dependencies
tidy:
	go mod tidy

# Generate coverage report
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

# CI pipeline shortcut (lint, tidy, test-race)
ci: lint tidy test-race
