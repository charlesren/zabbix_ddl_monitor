#!/bin/bash

# Test runner script for Manager module
# Usage: ./run_tests.sh [option]
# Options:
#   all       - Run all tests (default)
#   unit      - Run only unit tests
#   integration - Run only integration tests
#   bench     - Run benchmark tests
#   coverage  - Generate coverage report
#   race      - Run tests with race detection
#   short     - Run tests in short mode (skip long-running tests)
#   clean     - Clean test artifacts

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
TEST_TYPE="all"
VERBOSE="-v"
PACKAGE="."

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        all|unit|integration|bench|coverage|race|short|clean)
            TEST_TYPE="$1"
            shift
            ;;
        -q|--quiet)
            VERBOSE=""
            shift
            ;;
        -p|--package)
            PACKAGE="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [option]"
            echo "Options:"
            echo "  all         - Run all tests (default)"
            echo "  unit        - Run only unit tests"
            echo "  integration - Run only integration tests"
            echo "  bench       - Run benchmark tests"
            echo "  coverage    - Generate coverage report"
            echo "  race        - Run tests with race detection"
            echo "  short       - Run tests in short mode"
            echo "  clean       - Clean test artifacts"
            echo "  -q, --quiet - Run tests without verbose output"
            echo "  -p, --package - Specify package to test (default: .)"
            echo "  -h, --help  - Show this help message"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if go is installed
check_go() {
    if ! command -v go &> /dev/null; then
        print_error "Go is not installed or not in PATH"
        exit 1
    fi
    print_status "Using Go version: $(go version)"
}

# Function to install test dependencies
install_deps() {
    print_status "Installing test dependencies..."
    go mod download
    if ! go list -m github.com/stretchr/testify &> /dev/null; then
        print_status "Installing testify..."
        go get github.com/stretchr/testify
    fi
}

# Function to clean test artifacts
clean_artifacts() {
    print_status "Cleaning test artifacts..."
    rm -f coverage.out coverage.html
    rm -f cpu.prof mem.prof
    rm -f test_output.log
    print_success "Test artifacts cleaned"
}

# Function to run unit tests
run_unit_tests() {
    print_status "Running unit tests..."

    local test_files=(
        "manager_test.go"
        "router_scheduler_test.go"
        "interval_queue_test.go"
    )

    for test_file in "${test_files[@]}"; do
        if [[ -f "$test_file" ]]; then
            print_status "Running tests in $test_file"
            go test $VERBOSE -run "^Test" "$test_file" || {
                print_error "Unit tests failed in $test_file"
                return 1
            }
        else
            print_warning "Test file $test_file not found"
        fi
    done

    print_success "All unit tests passed"
}

# Function to run integration tests
run_integration_tests() {
    print_status "Running integration tests..."

    if [[ -f "integration_test.go" ]]; then
        go test $VERBOSE -run "Integration" integration_test.go || {
            print_error "Integration tests failed"
            return 1
        }
        print_success "Integration tests passed"
    else
        print_warning "Integration test file not found"
    fi
}

# Function to run benchmark tests
run_benchmark_tests() {
    print_status "Running benchmark tests..."

    # Run benchmarks and save output
    go test -bench=. -benchmem -benchtime=5s > bench_results.txt 2>&1 || {
        print_error "Benchmark tests failed"
        return 1
    }

    print_success "Benchmark tests completed"
    print_status "Benchmark results saved to bench_results.txt"

    # Display summary
    if command -v grep &> /dev/null; then
        echo
        print_status "Benchmark Summary:"
        grep -E "^Benchmark" bench_results.txt | head -10
    fi
}

# Function to generate coverage report
run_coverage() {
    print_status "Generating coverage report..."

    # Run tests with coverage
    go test -coverprofile=coverage.out $PACKAGE/... || {
        print_error "Coverage tests failed"
        return 1
    }

    # Generate text report
    go tool cover -func=coverage.out > coverage_summary.txt

    # Generate HTML report
    go tool cover -html=coverage.out -o coverage.html

    # Display summary
    local coverage_percent=$(go tool cover -func=coverage.out | tail -1 | awk '{print $3}')
    print_success "Coverage report generated: $coverage_percent"
    print_status "HTML report: coverage.html"
    print_status "Text summary: coverage_summary.txt"

    # Check coverage threshold
    local coverage_num=$(echo $coverage_percent | sed 's/%//')
    if (( $(echo "$coverage_num >= 80" | bc -l) )); then
        print_success "Coverage meets threshold (80%): $coverage_percent"
    else
        print_warning "Coverage below threshold (80%): $coverage_percent"
    fi
}

# Function to run tests with race detection
run_race_tests() {
    print_status "Running tests with race detection..."

    go test $VERBOSE -race $PACKAGE/... || {
        print_error "Race detection tests failed"
        return 1
    }

    print_success "No race conditions detected"
}

# Function to run tests in short mode
run_short_tests() {
    print_status "Running tests in short mode (skipping long-running tests)..."

    go test $VERBOSE -short $PACKAGE/... || {
        print_error "Short tests failed"
        return 1
    }

    print_success "Short tests passed"
}

# Function to run all tests
run_all_tests() {
    print_status "Running all tests..."

    # Check for specific test patterns
    local test_patterns=(
        "^TestManager"
        "^TestRouterScheduler"
        "^TestIntervalTaskQueue"
        "Integration"
    )

    for pattern in "${test_patterns[@]}"; do
        print_status "Running tests matching pattern: $pattern"
        go test $VERBOSE -run "$pattern" $PACKAGE/... || {
            print_error "Tests failed for pattern: $pattern"
            return 1
        }
    done

    print_success "All tests passed"
}

# Function to validate test environment
validate_environment() {
    print_status "Validating test environment..."

    # Check if we're in the right directory
    if [[ ! -f "manager.go" ]]; then
        print_error "manager.go not found. Please run from the manager directory."
        exit 1
    fi

    # Check if test files exist
    local test_files=(
        "manager_test.go"
        "router_scheduler_test.go"
        "interval_queue_test.go"
    )

    local missing_files=()
    for file in "${test_files[@]}"; do
        if [[ ! -f "$file" ]]; then
            missing_files+=("$file")
        fi
    done

    if [[ ${#missing_files[@]} -gt 0 ]]; then
        print_warning "Missing test files: ${missing_files[*]}"
    fi

    print_success "Environment validation complete"
}

# Function to setup test environment
setup_test_env() {
    print_status "Setting up test environment..."

    # Set test timeout
    export GO_TEST_TIMEOUT=${GO_TEST_TIMEOUT:-"300s"}

    # Set parallel test count
    export GOMAXPROCS=${GOMAXPROCS:-$(nproc 2>/dev/null || echo 4)}

    print_status "Test timeout: $GO_TEST_TIMEOUT"
    print_status "Max parallel processes: $GOMAXPROCS"
}

# Function to generate test report
generate_report() {
    print_status "Generating test report..."

    local report_file="test_report.md"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')

    cat > "$report_file" << EOF
# Test Report

**Generated:** $timestamp
**Go Version:** $(go version)
**Test Type:** $TEST_TYPE

## Summary

EOF

    if [[ -f "coverage_summary.txt" ]]; then
        echo "### Coverage" >> "$report_file"
        echo "\`\`\`" >> "$report_file"
        tail -5 coverage_summary.txt >> "$report_file"
        echo "\`\`\`" >> "$report_file"
        echo >> "$report_file"
    fi

    if [[ -f "bench_results.txt" ]]; then
        echo "### Benchmarks" >> "$report_file"
        echo "\`\`\`" >> "$report_file"
        grep -E "^Benchmark" bench_results.txt | head -10 >> "$report_file"
        echo "\`\`\`" >> "$report_file"
    fi

    print_success "Test report generated: $report_file"
}

# Main execution
main() {
    print_status "Manager Test Runner"
    print_status "=================="

    # Check prerequisites
    check_go
    validate_environment
    setup_test_env
    install_deps

    # Execute based on test type
    case $TEST_TYPE in
        "unit")
            run_unit_tests
            ;;
        "integration")
            run_integration_tests
            ;;
        "bench")
            run_benchmark_tests
            ;;
        "coverage")
            run_coverage
            ;;
        "race")
            run_race_tests
            ;;
        "short")
            run_short_tests
            ;;
        "clean")
            clean_artifacts
            exit 0
            ;;
        "all"|*)
            run_all_tests
            ;;
    esac

    # Generate report for non-clean operations
    if [[ "$TEST_TYPE" != "clean" ]]; then
        generate_report
    fi

    print_success "Test execution completed successfully!"
}

# Trap to handle script interruption
trap 'print_error "Test execution interrupted"; exit 1' INT TERM

# Run main function
main "$@"
