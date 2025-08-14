# Zabbix DDL Monitor

A Go-based service that monitors dedicated line connectivity via Zabbix API integration, leveraging `scrapligo` and SSH protocols for router interactions.

## Overview

The Zabbix DDL Monitor is a comprehensive network monitoring solution that automatically discovers and monitors dedicated lines (DDL) through Zabbix infrastructure. It provides real-time connectivity monitoring by executing ping tasks on edge routers and reporting results back to monitoring systems.

## Key Features

### 🔄 Dynamic Configuration Management
- **Zabbix API Integration**: Automatically discovers dedicated lines from Zabbix hosts using proxy-based filtering
- **Real-time Synchronization**: Monitors configuration changes and adapts monitoring tasks dynamically
- **Tag-based Filtering**: Uses Zabbix host tags (`TempType=LINE`) to identify dedicated line hosts
- **Macro-driven Configuration**: Extracts line and router details from Zabbix host macros

### 🚀 Scalable Task System
- **Plugin Architecture**: Extensible task system supporting multiple monitoring types
- **Platform Agnostic**: Supports multiple router platforms (Cisco IOSXE, Huawei VRP, H3C Comware, etc.)
- **Protocol Flexibility**: Dual protocol support (SSH and Scrapli) for different use cases
- **Batch Processing**: Efficient batch ping operations for multiple target IPs

### 🔗 Advanced Connection Management
- **Connection Pooling**: Efficient resource management with automatic cleanup and health checks
- **Multi-protocol Support**: SSH for basic operations, Scrapli for advanced interactive features
- **Capability Validation**: Automatic validation of protocol/platform/command compatibility
- **Resilience**: Built-in connection retry, timeout handling, and graceful degradation

### 📊 Robust Execution Framework
- **Middleware Support**: Configurable timeout, retry, logging, and metrics collection
- **Async Execution**: Non-blocking task execution with worker pools
- **Result Aggregation**: Comprehensive result collection and reporting
- **Error Handling**: Detailed error tracking and recovery mechanisms

## Architecture Components

### Core Modules

#### ConfigSyncer
- Fetches line configurations from Zabbix API
- Monitors configuration changes via subscription model
- Manages configuration versions and change notifications
- Handles proxy-based host discovery and filtering

#### Manager
- Central orchestration component
- Manages router schedulers based on line configurations
- Handles full and incremental synchronization
- Coordinates lifecycle of monitoring components

#### Connection System
- **ConnectionPool**: Manages protocol driver instances with pooling
- **ProtocolDriver**: Interface for SSH and Scrapli protocol implementations
- **Factory Pattern**: Creates appropriate drivers based on configuration
- **Capability System**: Validates platform and protocol compatibility

#### Task Framework
- **Task Interface**: Plugin system for different monitoring types
- **PingTask**: Primary implementation for connectivity monitoring
- **TaskRegistry**: Central registry for available tasks
- **Executor**: Middleware-based task execution engine

## Supported Platforms & Protocols

### Router Platforms
- **Cisco IOSXE**: Full support with interactive and command modes
- **Cisco NXOS**: Scrapli protocol support
- **Huawei VRP**: SSH and Scrapli protocol support
- **H3C Comware**: Basic protocol support
- **Cisco IOSXR**: Extended platform support

### Protocol Support
- **SSH**: Basic command execution, universal compatibility
- **Scrapli**: Advanced features including interactive events, auto-completion

### Command Types
- **Commands**: Simple command execution
- **Interactive Events**: Complex interactive sessions with prompt handling

## Configuration

### Zabbix Integration
The system requires specific Zabbix host macros for line configuration:

```yaml
# Required macros on dedicated line hosts
{$LINE_ID}: "unique-line-identifier"
{$LINE_CHECK_INTERVAL}: "180"  # seconds
{$LINE_ROUTER_IP}: "192.168.1.1"
{$LINE_ROUTER_USERNAME}: "admin"
{$LINE_ROUTER_PASSWORD}: "password"
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"
{$LINE_ROUTER_PROTOCOL}: "scrapli"
```

### Service Configuration
```yaml
# conf/svr.yml
server:
  log:
    applog:
      loglevel: 1
  ip: 192.168.1.100

zabbix:
  username: "monitor_user"
  password: "password"
  serverip: "zabbix.example.com"
  serverport: "80"
```

## Usage

### Starting the Service
```bash
# Default configuration
./cmd/monitor/main

# Custom configuration path
./cmd/monitor/main -c /path/to/config.yml
```

### Task Examples

#### Single IP Ping
```go
params := map[string]interface{}{
    "target_ip": "8.8.8.8",
    "repeat": 5,
    "timeout": 2 * time.Second,
}
```

#### Batch IP Ping
```go
params := map[string]interface{}{
    "target_ips": []string{"8.8.8.8", "1.1.1.1", "208.67.222.222"},
    "repeat": 3,
    "timeout": 1 * time.Second,
}
```

## API Reference

### Task Interface
```go
type Task interface {
    Meta() TaskMeta
    ValidateParams(params map[string]interface{}) error
    BuildCommand(ctx TaskContext) (Command, error)
    ParseOutput(ctx TaskContext, raw interface{}) (Result, error)
}
```

### Protocol Driver Interface
```go
type ProtocolDriver interface {
    ProtocolType() Protocol
    Close() error
    Execute(req *ProtocolRequest) (*ProtocolResponse, error)
    GetCapability() ProtocolCapability
}
```

## Monitoring & Metrics

### Health Checks
- Connection pool health monitoring
- Protocol driver capability validation
- Task execution success rates
- Configuration synchronization status

### Logging
- Structured logging with configurable levels
- Task execution tracing
- Connection lifecycle events
- Error tracking and debugging

### Performance Metrics
- Task execution duration
- Connection pool utilization
- Success/failure rates by platform
- Configuration sync frequency

## Development

### Building
```bash
go mod download
go build -o ddl_monitor ./cmd/monitor
```

### Testing
```bash
# Unit tests
go test ./...

# Integration tests
go test ./connection -tags=integration
go test ./task -tags=integration
```

### Extending the System

#### Adding New Platforms
1. Implement platform-specific command generation in task adapters
2. Add platform constant to `connection/types.go`
3. Update capability definitions
4. Add platform-specific output parsing

#### Adding New Task Types
1. Implement the `Task` interface
2. Register task in `TaskRegistry`
3. Add platform-specific implementations
4. Update capability mappings

## Dependencies

### Core Dependencies
- **scrapligo**: Advanced network device automation
- **zapix**: Zabbix API client library
- **ylog**: Structured logging framework
- **viper**: Configuration management

### Protocol Libraries
- **golang.org/x/crypto/ssh**: SSH protocol implementation
- **scrapli/scrapligo**: Network device automation framework

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Push to the branch
5. Create a Pull Request

## Support

For issues and questions:
- Create an issue in the GitHub repository
- Check the documentation in the `/docs` directory
- Review test files for usage examples
