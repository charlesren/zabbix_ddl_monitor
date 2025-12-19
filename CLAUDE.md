# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Zabbix DDL (Dedicated Line) Monitor - an enterprise-grade network monitoring system written in Go (1.24.5). It monitors dedicated line connectivity by executing ping tests on edge routers and reporting results back to Zabbix through proxy infrastructure.

## Key Architecture Components

### Core Modules
- **`cmd/monitor/`** - Main application entry point (`main.go`)
- **`connection/`** - Connection management with protocol drivers (SSH/Scrapli), connection pooling, and resilience features
- **`manager/`** - Central orchestration component that coordinates router schedulers and lifecycle management
- **`syncer/`** - Configuration synchronization from Zabbix API with change monitoring
- **`task/`** - Task framework with PingTask implementation, async execution, and result aggregation

### Data Flow
```
Zabbix API → ConfigSyncer → Manager → RouterScheduler → Task → Aggregator → ZabbixSender → Zabbix Proxy
```

### Design Philosophy
- **Single-IP processing**: Each dedicated line executes ping tasks independently for better error isolation and connection reuse
- **Connection pooling**: Efficient resource management with protocol-specific optimizations
- **Async execution**: Non-blocking task execution with smart timeout management
- **Dynamic configuration**: Hot reload from Zabbix API with zero downtime

## Development Commands

### Building and Running
```bash
# Download dependencies
go mod download

# Build the application
go build -o ddl_monitor ./cmd/monitor

# Create logs directory (required)
mkdir -p logs

# Run with default config (relative to executable)
./ddl_monitor

# Run with custom config
./ddl_monitor -c /path/to/config.yml

# Run directly with Go
go run ./cmd/monitor/main.go -c conf/svr.yml
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html

# Run tests with race detection
go test -race ./...

# Run integration tests (requires network devices)
go test ./connection -tags=integration
go test ./task -tags=integration

# Run specific module tests
cd manager && make test
cd syncer && make test
```

### Code Quality
```bash
# Format code
go fmt ./...

# Vet code
go vet ./...

# Check dependencies
go mod tidy
```

## Configuration Requirements

### Zabbix Host Macros (Required)
Each dedicated line host in Zabbix must have these macros:
```yaml
{$LINE_ID}: "unique-line-identifier"      # Unique line identifier
{$LINE_CHECK_INTERVAL}: "180"             # Check interval in seconds
{$LINE_ROUTER_IP}: "192.168.1.1"         # Router IP address
{$LINE_ROUTER_USERNAME}: "admin"          # Router username
{$LINE_ROUTER_PASSWORD}: "password"       # Router password
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"   # Router platform
{$LINE_ROUTER_PROTOCOL}: "scrapli"       # Protocol type (ssh or scrapli)
```

### Service Configuration (`conf/svr.yml`)
```yaml
server:
  log:
    applog:
      loglevel: 1                 # 0=Debug, 1=Info, 2=Warn, 3=Error

zabbix:
  username: "aoms"                # Zabbix username
  password: "your_password"       # Zabbix password
  serverip: "10.194.75.135"       # Zabbix server IP
  serverport: "80"                # Zabbix server port
  proxyname: "zabbix-proxy-01"    # REQUIRED: Proxy name for host discovery
  proxyip: "10.194.75.134"        # REQUIRED: Proxy IP for data submission
  proxyport: "10051"              # REQUIRED: Proxy port for data submission
```

**Note**: Log file path is hardcoded to `../logs/ddl_monitor.log` relative to the executable.

## Supported Platforms and Protocols

### Router Platforms
- Cisco IOSXE (full support with interactive and command modes)
- Cisco NXOS (Scrapli protocol support)
- Huawei VRP (SSH and Scrapli protocol support)
- H3C Comware (basic protocol support)
- Cisco IOSXR (extended platform support)

### Protocol Support
- **SSH**: Basic command execution, universal compatibility
- **Scrapli**: Advanced features including interactive events, auto-completion

## Key Implementation Patterns

### Connection Management (`connection/`)
- Uses builder pattern for configuration (`NewConfigBuilder()`)
- Enhanced connection pool with load balancing and health checks
- Protocol drivers implement `ProtocolDriver` interface
- Resilience features: retry policies, circuit breakers, fallbacks
- Comprehensive metrics collection and monitoring

### Task Framework (`task/`)
- Plugin architecture with `Task` interface
- `PingTask` is the primary implementation for connectivity monitoring
- Async execution with middleware support (timeout, retry, logging)
- Result aggregation and batch processing
- Zabbix sender with connection pooling

### Configuration Synchronization (`syncer/`)
- Fetches line configurations from Zabbix API using proxy-based filtering
- Monitors configuration changes via subscription model
- Uses host tags (`TempType=LINE`) to identify dedicated line hosts
- Extracts configuration from Zabbix host macros

### Manager (`manager/`)
- Central coordinator that subscribes to configuration changes
- Creates/deletes router schedulers based on line configurations
- Manages interval-based task queues
- Handles full and incremental synchronization

## Important Code Locations

### Entry Points
- `cmd/monitor/main.go:main()` - Main application entry with panic recovery and graceful shutdown
- `manager/manager.go:NewManager()` - Core orchestration component creation
- `syncer/syncer.go:NewConfigSyncer()` - Configuration synchronization setup

### Key Interfaces
- `connection/types.go` - Protocol and platform definitions, `ProtocolDriver` interface
- `task/task.go` - `Task` interface and related types
- `syncer/types.go` - Line configuration and change event types

### Configuration Loading
- `cmd/monitor/main.go:initConfig()` - Loads config from `conf/svr.yml`
- Uses `github.com/charlesren/userconfig` for configuration management
- Log system initialized with hardcoded path `../logs/ddl_monitor.log`

## Error Handling and Resilience

### Global Panic Recovery
- `main.go` has a global panic recovery that logs stack traces before exit
- `safeStart()` function wraps goroutine execution with panic recovery
- Health monitoring tracks goroutine counts and memory usage

### Connection Resilience
- Exponential backoff retry policies
- Circuit breaker pattern for fault isolation
- Connection pool health checks and automatic recovery
- Graceful degradation on failures

### Task Execution
- Async execution with timeout management
- Result aggregation with batch processing
- Comprehensive error logging and metrics collection

## Testing Strategy

### Unit Tests
- Comprehensive test coverage across all modules
- Mock implementations for external dependencies
- Test utilities for common patterns

### Integration Tests
- Tagged with `integration` build tag
- Require actual network devices or test environment
- Test protocol drivers and task execution

### Makefile Targets
- `manager/Makefile` and `syncer/Makefile` provide module-specific test commands
- Coverage analysis, race detection, and benchmark tests
- Code quality checks (fmt, vet, lint)

## Development Notes

### Dependencies
- `github.com/scrapli/scrapligo` - Network device automation (forked version)
- `github.com/charlesren/zapix` - Custom Zabbix API client
- `github.com/charlesren/ylog` - Structured logging framework
- `github.com/charlesren/userconfig` - Configuration management
- `github.com/spf13/viper` - Configuration parsing

### Code Organization
- Each module has its own `types.go` for type definitions
- Factories and builders used for complex object creation
- Interface-based design for extensibility
- Comprehensive documentation in README files

### Performance Considerations
- Connection pooling reduces connection establishment overhead
- Async execution prevents blocking on slow network operations
- Result aggregation minimizes Zabbix API calls
- Memory monitoring and leak detection in health checks

## Troubleshooting

### Common Issues
1. **Connection failures**: Check router credentials and network connectivity
2. **Zabbix API errors**: Verify Zabbix server configuration and credentials
3. **Missing logs**: Ensure `logs` directory exists in parent directory of executable
4. **Configuration sync failures**: Check proxy name and Zabbix host tags

### Debug Mode
```bash
# Enable Zabbix API debugging
DEBUG=on ./ddl_monitor -c conf/svr.yml
```

### Health Monitoring
- System monitors goroutine counts for leaks
- Memory usage tracked and logged hourly
- Connection pool statistics available via metrics
- Detailed error logging with stack traces on panics

## Extension Points

### Adding New Platforms
1. Add platform constant to `connection/types.go`
2. Implement platform-specific command generation in task adapters
3. Update capability definitions
4. Add platform-specific output parsing

### Adding New Task Types
1. Implement `Task` interface
2. Register task in `TaskRegistry`
3. Add platform-specific implementations
4. Update capability mappings

### Custom Protocol Drivers
1. Implement `ProtocolDriver` interface
2. Create corresponding `ProtocolFactory`
3. Register factory with connection pool
4. Update configuration builder to support new protocol