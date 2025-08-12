# Makefile for syncer package tests

.PHONY: test test-unit test-integration test-bench test-coverage test-verbose test-race clean help

# Default target
all: test

# Run all tests
test:
	@echo "Running all tests..."
	go test -v ./...

# Run unit tests only (exclude integration tests)
test-unit:
	@echo "Running unit tests..."
	go test -v -run "^Test[^I]" .

# Run integration tests only
test-integration:
	@echo "Running integration tests..."
	go test -v -run "TestIntegration" .

# Run error handling tests
test-error-handling:
	@echo "Running error handling tests..."
	go test -v -run "TestError" .

# Run coverage tests
test-coverage-analysis:
	@echo "Running coverage analysis tests..."
	go test -v -run "TestCoverage" .

# Run concurrency tests
test-concurrency:
	@echo "Running concurrency tests..."
	go test -v -run "TestConcurrency" .

# Run performance tests
test-performance:
	@echo "Running performance tests..."
	go test -v -run "TestPerformance" .

# Run benchmarks
test-bench:
	@echo "Running benchmarks..."
	go test -v -bench=. -benchmem .

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out -covermode=atomic .
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Run tests with race detection
test-race:
	@echo "Running tests with race detection..."
	go test -v -race .

# Run tests in verbose mode with timeout
test-verbose:
	@echo "Running verbose tests..."
	go test -v -timeout=60s .

# Run specific test
test-single:
	@echo "Running specific test: $(TEST)"
	go test -v -run "$(TEST)" .

# Run tests continuously (watch mode)
test-watch:
	@echo "Running tests in watch mode..."
	@command -v entr >/dev/null 2>&1 || { echo "entr is required for watch mode. Install it with: brew install entr (macOS) or apt-get install entr (Ubuntu)"; exit 1; }
	find . -name "*.go" | entr -c go test -v .

# Generate test mocks
generate-mocks:
	@echo "Generating mocks..."
	@command -v mockgen >/dev/null 2>&1 || { echo "mockgen is required. Install it with: go install github.com/golang/mock/mockgen@latest"; exit 1; }
	mockgen -source=types.go -destination=mocks/mock_client.go -package=mocks

# Clean test artifacts
clean:
	@echo "Cleaning test artifacts..."
	rm -f coverage.out coverage.html
	rm -f *.test
	rm -rf mocks/

# Run comprehensive coverage tests
test-comprehensive:
	@echo "Running comprehensive test suite..."
	go test -v -run "TestCoverageAnalysis" .
	go test -v -run "TestCoverageReport" .

# Run tests with different Go versions (requires Docker)
test-go-versions:
	@echo "Testing with Go 1.19..."
	docker run --rm -v $(PWD):/src -w /src golang:1.19 go test -v .
	@echo "Testing with Go 1.20..."
	docker run --rm -v $(PWD):/src -w /src golang:1.20 go test -v .
	@echo "Testing with Go 1.21..."
	docker run --rm -v $(PWD):/src -w /src golang:1.21 go test -v .

# Lint code
lint:
	@echo "Running golint..."
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint is required. Install it from: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run

# Format code
fmt:
	@echo "Formatting code..."
	go fmt .
	goimports -w .

# Vet code
vet:
	@echo "Vetting code..."
	go vet .

# Run all quality checks
quality: fmt vet lint test-race test-coverage

# Profile tests
profile-cpu:
	@echo "Profiling CPU usage..."
	go test -cpuprofile=cpu.prof -memprofile=mem.prof -bench=.

# Profile memory
profile-mem:
	@echo "Profiling memory usage..."
	go test -memprofile=mem.prof -bench=.

# View CPU profile
view-cpu-profile:
	@echo "Viewing CPU profile..."
	go tool pprof cpu.prof

# View memory profile
view-mem-profile:
	@echo "Viewing memory profile..."
	go tool pprof mem.prof

# Install test dependencies
install-deps:
	@echo "Installing test dependencies..."
	go install github.com/stretchr/testify@latest
	go install github.com/golang/mock/mockgen@latest
	go install golang.org/x/tools/cmd/goimports@latest

# Run mutation testing
test-mutation:
	@echo "Running mutation testing..."
	@command -v go-mutesting >/dev/null 2>&1 || { echo "go-mutesting is required. Install it with: go install github.com/zimmski/go-mutesting/cmd/go-mutesting@latest"; exit 1; }
	go-mutesting .

# Run fuzz tests (Go 1.18+)
test-fuzz:
	@echo "Running fuzz tests..."
	go test -fuzz=.

# Test with different build tags
test-tags:
	@echo "Testing with integration tag..."
	go test -v -tags=integration .

# Generate test report
test-report:
	@echo "Generating test report..."
	go test -json . > test-results.json
	@echo "Test results saved to: test-results.json"

# Generate comprehensive test report
test-report-comprehensive:
	@echo "Generating comprehensive test report..."
	go test -v -run "TestCoverageReport" . > test-coverage-report.txt
	go test -json . > test-results-full.json
	@echo "Comprehensive test report saved to: test-coverage-report.txt"
	@echo "Full test results saved to: test-results-full.json"

# Check test coverage threshold
check-coverage:
	@echo "Checking coverage threshold..."
	@go test -coverprofile=coverage.out . > /dev/null 2>&1
	@COVERAGE=$$(go tool cover -func=coverage.out | grep total | grep -Eo '[0-9]+\.[0-9]+'); \
	THRESHOLD=80.0; \
	if [ $$(echo "$$COVERAGE >= $$THRESHOLD" | bc -l) -eq 1 ]; then \
		echo "✓ Coverage $$COVERAGE% meets threshold of $$THRESHOLD%"; \
	else \
		echo "✗ Coverage $$COVERAGE% is below threshold of $$THRESHOLD%"; \
		exit 1; \
	fi

# Help target
help:
	@echo "Available targets:"
	@echo "  test              - Run all tests"
	@echo "  test-unit         - Run unit tests only"
	@echo "  test-integration  - Run integration tests only"
	@echo "  test-error-handling - Run error handling tests"
	@echo "  test-concurrency  - Run concurrency tests"
	@echo "  test-performance  - Run performance tests"
	@echo "  test-coverage-analysis - Run coverage analysis tests"
	@echo "  test-comprehensive - Run comprehensive test suite"
	@echo "  test-bench        - Run benchmarks"
	@echo "  test-coverage     - Run tests with coverage"
	@echo "  test-race         - Run tests with race detection"
	@echo "  test-verbose      - Run tests in verbose mode"
	@echo "  test-single       - Run specific test (use TEST=TestName)"
	@echo "  test-watch        - Run tests continuously"
	@echo "  test-fuzz         - Run fuzz tests"
	@echo "  generate-mocks    - Generate test mocks"
	@echo "  clean             - Clean test artifacts"
	@echo "  lint              - Lint code"
	@echo "  fmt               - Format code"
	@echo "  vet               - Vet code"
	@echo "  quality           - Run all quality checks"
	@echo "  install-deps      - Install test dependencies"
	@echo "  check-coverage    - Check coverage threshold"
	@echo "  test-report-comprehensive - Generate comprehensive test report"
	@echo "  help              - Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  make test"
	@echo "  make test-single TEST=TestConfigSyncer_Start"
	@echo "  make test-coverage"
	@echo "  make test-race"
	@echo "  make test-error-handling"
	@echo "  make test-concurrency"
	@echo "  make test-performance"
	@echo "  make test-comprehensive"
