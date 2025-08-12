# Makefile for Manager module tests

.PHONY: test test-unit test-integration test-bench test-coverage test-race test-short clean help

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Test parameters
TEST_TIMEOUT=300s
TEST_PARALLEL=4
COVERAGE_OUT=coverage.out
COVERAGE_HTML=coverage.html

# Default target
all: test

# Help target
help:
	@echo "Available targets:"
	@echo "  test           - Run all tests (default)"
	@echo "  test-unit      - Run unit tests only"
	@echo "  test-integration - Run integration tests only"
	@echo "  test-bench     - Run benchmark tests"
	@echo "  test-coverage  - Generate test coverage report"
	@echo "  test-race      - Run tests with race detection"
	@echo "  test-short     - Run tests in short mode"
	@echo "  test-verbose   - Run tests with verbose output"
	@echo "  clean          - Clean test artifacts"
	@echo "  deps           - Download dependencies"
	@echo "  fmt            - Format code"
	@echo "  vet            - Run go vet"
	@echo "  lint           - Run linter (if available)"
	@echo "  check          - Run fmt, vet, and lint"
	@echo "  ci             - Run CI pipeline (fmt, vet, test, coverage)"

# Run all tests
test:
	@echo "Running all tests..."
	$(GOTEST) -v -timeout $(TEST_TIMEOUT) -parallel $(TEST_PARALLEL) ./...

# Run unit tests only
test-unit:
	@echo "Running unit tests..."
	$(GOTEST) -v -timeout $(TEST_TIMEOUT) -run "^Test(Manager|RouterScheduler|IntervalTaskQueue)" ./...

# Run integration tests only
test-integration:
	@echo "Running integration tests..."
	$(GOTEST) -v -timeout $(TEST_TIMEOUT) -run "Integration" ./...

# Run benchmark tests
test-bench:
	@echo "Running benchmark tests..."
	$(GOTEST) -bench=. -benchmem -benchtime=5s ./...

# Generate test coverage report
test-coverage:
	@echo "Generating coverage report..."
	$(GOTEST) -coverprofile=$(COVERAGE_OUT) ./...
	$(GOCMD) tool cover -html=$(COVERAGE_OUT) -o $(COVERAGE_HTML)
	$(GOCMD) tool cover -func=$(COVERAGE_OUT)
	@echo "Coverage report generated: $(COVERAGE_HTML)"

# Run tests with race detection
test-race:
	@echo "Running tests with race detection..."
	$(GOTEST) -v -race -timeout $(TEST_TIMEOUT) ./...

# Run tests in short mode (skip long-running tests)
test-short:
	@echo "Running tests in short mode..."
	$(GOTEST) -v -short -timeout $(TEST_TIMEOUT) ./...

# Run tests with verbose output
test-verbose:
	@echo "Running tests with verbose output..."
	$(GOTEST) -v -timeout $(TEST_TIMEOUT) -parallel $(TEST_PARALLEL) ./...

# Clean test artifacts
clean:
	@echo "Cleaning test artifacts..."
	rm -f $(COVERAGE_OUT) $(COVERAGE_HTML)
	rm -f coverage_summary.txt bench_results.txt test_report.md
	rm -f cpu.prof mem.prof trace.out
	$(GOCLEAN) -testcache

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Format code
fmt:
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

# Run go vet
vet:
	@echo "Running go vet..."
	$(GOCMD) vet ./...

# Run linter (if golangci-lint is available)
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, skipping..."; \
	fi

# Check code quality (fmt, vet, lint)
check: fmt vet lint
	@echo "Code quality check completed"

# CI pipeline
ci: check test-coverage test-race
	@echo "CI pipeline completed successfully"

# Quick test for development
quick:
	@echo "Running quick tests..."
	$(GOTEST) -v -short -timeout 60s ./...

# Test specific package
test-manager:
	@echo "Running Manager tests..."
	$(GOTEST) -v -run "TestManager" .

test-scheduler:
	@echo "Running RouterScheduler tests..."
	$(GOTEST) -v -run "TestRouterScheduler" .

test-queue:
	@echo "Running IntervalTaskQueue tests..."
	$(GOTEST) -v -run "TestIntervalTaskQueue" .

# Profile tests
profile-cpu:
	@echo "Running CPU profiling..."
	$(GOTEST) -cpuprofile=cpu.prof -bench=. ./...
	@echo "CPU profile saved to cpu.prof"

profile-mem:
	@echo "Running memory profiling..."
	$(GOTEST) -memprofile=mem.prof -bench=. ./...
	@echo "Memory profile saved to mem.prof"

profile-trace:
	@echo "Running trace profiling..."
	$(GOTEST) -trace=trace.out -bench=. ./...
	@echo "Trace profile saved to trace.out"

# Watch tests (requires entr or similar tool)
watch:
	@if command -v entr >/dev/null 2>&1; then \
		echo "Watching for changes..."; \
		find . -name "*.go" | entr -c make test; \
	else \
		echo "entr not found. Install with: brew install entr (macOS) or apt-get install entr (Ubuntu)"; \
	fi

# Generate test data
generate-testdata:
	@echo "Generating test data..."
	@mkdir -p testdata
	@echo "Test data generated in testdata/"

# Run tests with different Go versions (if available)
test-all-versions:
	@echo "Testing with different Go versions..."
	@for version in 1.19 1.20 1.21; do \
		if command -v go$$version >/dev/null 2>&1; then \
			echo "Testing with Go $$version..."; \
			go$$version test -v ./...; \
		fi; \
	done

# Performance regression test
test-regression:
	@echo "Running performance regression tests..."
	$(GOTEST) -bench=. -benchmem -count=5 ./... > bench_new.txt
	@if [ -f bench_old.txt ]; then \
		echo "Comparing with previous benchmark..."; \
		benchcmp bench_old.txt bench_new.txt; \
	else \
		echo "No previous benchmark found, saving current as baseline..."; \
	fi
	@mv bench_new.txt bench_old.txt

# Stress test
stress:
	@echo "Running stress tests..."
	$(GOTEST) -v -timeout 10m -run "Stress|Performance" ./...

# Run tests in Docker (if Dockerfile exists)
test-docker:
	@if [ -f Dockerfile ]; then \
		echo "Running tests in Docker..."; \
		docker build -t manager-tests .; \
		docker run --rm manager-tests make test; \
	else \
		echo "No Dockerfile found"; \
	fi

# Install test tools
install-tools:
	@echo "Installing test tools..."
	$(GOGET) github.com/stretchr/testify/assert
	$(GOGET) github.com/stretchr/testify/mock
	$(GOGET) github.com/stretchr/testify/require
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "Installing golangci-lint..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.54.2; \
	fi

# Check test coverage threshold
check-coverage:
	@echo "Checking coverage threshold..."
	@$(GOTEST) -coverprofile=$(COVERAGE_OUT) ./... >/dev/null 2>&1
	@coverage=$$($(GOCMD) tool cover -func=$(COVERAGE_OUT) | tail -1 | awk '{print $$3}' | sed 's/%//'); \
	if [ "$$(echo "$$coverage >= 80" | bc)" -eq 1 ]; then \
		echo "✓ Coverage meets threshold (80%): $$coverage%"; \
	else \
		echo "✗ Coverage below threshold (80%): $$coverage%"; \
		exit 1; \
	fi

# Generate test report
report:
	@echo "Generating test report..."
	@echo "# Test Report" > test_report.md
	@echo "" >> test_report.md
	@echo "**Generated:** $$(date)" >> test_report.md
	@echo "**Go Version:** $$(go version)" >> test_report.md
	@echo "" >> test_report.md
	@echo "## Test Results" >> test_report.md
	@echo "" >> test_report.md
	@$(GOTEST) -v ./... 2>&1 | grep -E "(PASS|FAIL)" >> test_report.md || true
	@echo "" >> test_report.md
	@if [ -f $(COVERAGE_OUT) ]; then \
		echo "## Coverage Report" >> test_report.md; \
		echo "" >> test_report.md; \
		echo "\`\`\`" >> test_report.md; \
		$(GOCMD) tool cover -func=$(COVERAGE_OUT) | tail -5 >> test_report.md; \
		echo "\`\`\`" >> test_report.md; \
	fi
	@echo "Test report generated: test_report.md"

# Show test statistics
stats:
	@echo "Test Statistics:"
	@echo "==============="
	@echo -n "Total test files: "
	@find . -name "*_test.go" | wc -l
	@echo -n "Total test functions: "
	@grep -r "^func Test" . --include="*_test.go" | wc -l
	@echo -n "Total benchmark functions: "
	@grep -r "^func Benchmark" . --include="*_test.go" | wc -l
	@echo -n "Lines of test code: "
	@find . -name "*_test.go" -exec cat {} \; | wc -l
