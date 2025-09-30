# Zabbix DDL Monitor

A Go-based service that monitors dedicated line connectivity via Zabbix API integration, leveraging `scrapligo` and SSH protocols for router interactions.

## Overview

The Zabbix DDL Monitor is a comprehensive network monitoring solution that automatically discovers and monitors dedicated lines (DDL) through Zabbix infrastructure. It provides real-time connectivity monitoring by executing individual ping tasks on edge routers and reporting results back to monitoring systems through the Zabbix proxy infrastructure.

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
- **Individual IP Processing**: Each dedicated line executes ping tasks independently for better error isolation and connection reuse efficiency

### 🔗 Advanced Connection Management
- **Connection Pooling**: Efficient resource management with automatic cleanup and health checks
- **Multi-protocol Support**: SSH for basic operations, Scrapli for advanced interactive features
- **Capability Validation**: Automatic validation of protocol/platform/command compatibility
- **Resilience**: Built-in connection retry, timeout handling, and graceful degradation

### 📊 Robust Execution Framework
- **Middleware Support**: Configurable timeout, retry, logging, and metrics collection
- **Async Execution**: Non-blocking task execution with smart timeout management and worker pools
- **Result Aggregation**: Comprehensive result collection and reporting
- **Error Handling**: Detailed error tracking and recovery mechanisms

## Architecture Components

### Project Structure
```
zabbix_ddl_monitor/
├── cmd/              # Main application entry points
├── conf/             # Configuration files
├── connection/       # Connection management and protocol drivers
├── docs/             # Documentation
├── manager/          # Central orchestration and scheduling
├── spec/             # Technical specifications
├── syncer/           # Configuration synchronization from Zabbix
└── task/             # Task framework and implementations
```

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
- **Task Interface**: Plugin system for different monitoring types with platform-specific implementations
- **PingTask**: Primary implementation for connectivity monitoring with adaptive command generation
- **TaskRegistry**: Central registry for available tasks with auto-discovery capabilities
- **AsyncExecutor**: Middleware-based async task execution engine with smart timeout management
- **ResultAggregator**: Batch result collection and submission to monitoring systems

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
{$LINE_ID}: "unique-line-identifier"      # Unique line identifier
{$LINE_CHECK_INTERVAL}: "180"             # Check interval in seconds
{$LINE_ROUTER_IP}: "192.168.1.1"         # Router IP address
{$LINE_ROUTER_USERNAME}: "admin"          # Router username
{$LINE_ROUTER_PASSWORD}: "password"       # Router password
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"   # Router platform
{$LINE_ROUTER_PROTOCOL}: "scrapli"       # Protocol type
```

### Service Configuration
```yaml
# conf/svr.yml
server:
  log:
    applog:
      loglevel: 1                 # Log level: 0=Debug, 1=Info, 2=Warn, 3=Error
  ip: xx.xx.xx.xx

zabbix:
  username: "aoms"
  password: "your_password"
  serverip: "10.194.75.135"
  serverport: "80"
  proxyname: "zabbix-proxy-01"    # Required: Proxy name for host discovery
  proxyip: "10.194.75.134"        # Required: Proxy IP for data submission
  proxyport: "10051"              # Required: Proxy port for data submission (configurable)
```

**Note**: The log file path is hardcoded to `../logs/ddl_monitor.log` (relative to the executable). Make sure the `logs` directory exists in the parent directory of where you run the application.

## Usage

### Starting the Service
```bash
# Build the application first
go mod download
go build -o ddl_monitor ./cmd/monitor

# Create logs directory
mkdir -p logs

# Default configuration (uses ../conf/svr.yml relative to executable)
./ddl_monitor

# Custom configuration path
./ddl_monitor -c /path/to/config.yml

# Alternative: Run directly
go run ./cmd/monitor/main.go -c conf/svr.yml
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

> **Note**: Current system implementation uses individual IP processing mode, where each dedicated line executes ping tasks independently for better error isolation and connection reuse efficiency.

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
# Download dependencies
go mod download

# Build the application
go build -o ddl_monitor ./cmd/monitor

# Or build with specific output directory
go build -o bin/ddl_monitor ./cmd/monitor
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
- **github.com/scrapli/scrapligo**: Advanced network device automation and protocol drivers
- **github.com/charlesren/zapix**: Custom Zabbix API client library with enhanced features
- **github.com/charlesren/ylog**: Structured logging framework with rotation support
- **github.com/charlesren/userconfig**: Configuration management with multi-format support
- **github.com/spf13/viper**: Configuration file parsing and management

### Protocol Libraries
- **golang.org/x/crypto/ssh**: SSH protocol implementation for basic router connections
- **github.com/scrapli/scrapligo**: Advanced network device automation with interactive support

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

## Architecture Design Notes

### 🔄 **Individual IP Processing Architecture**

The system implements an individual IP processing mode for maximum stability and efficiency:

**Design Advantages**:
- ✅ **Error Isolation**: Each dedicated line ping task executes independently, preventing cascading failures
- ✅ **Connection Efficiency**: Advanced connection pool management with protocol-specific optimizations
- ✅ **Simplified Parsing**: Direct command-response mapping eliminates complex batch output parsing
- ✅ **Scalable Design**: Independent parameter configuration and error handling per line
- ✅ **Debugging Friendly**: Each task has isolated execution context and detailed logging

**Why Individual Processing Over Batch**:
- Most network devices lack native batch ping command support
- Simplified error handling and recovery mechanisms  
- Better resource utilization through smart connection pooling
- Platform-agnostic implementation across different router types

### 🏗️ **Implementation Architecture**
```go
// Current execution logic in RouterScheduler.executeIndividualPing()
func (s *RouterScheduler) executeIndividualPing(line syncer.Line, task task.Task, cmdType task.CommandType) {
    // Get connection from pool
    conn, err := s.connection.Get(s.router.Protocol)
    if err != nil {
        // Handle connection failure
        return
    }
    
    // Create isolated task context for single IP
    taskCtx := task.TaskContext{
        TaskType: "ping",
        Platform: s.router.Platform,
        Protocol: s.router.Protocol,
        CommandType: cmdType,
        Params: map[string]interface{}{
            "target_ip": line.IP,    // Single target IP
            "repeat":    5,
            "timeout":   10 * time.Second,
        },
        Ctx: s.routerCtx,
    }
    
    // Submit to async executor with callback handling
    err = s.asyncExecutor.Submit(task, conn, taskCtx, func(result task.Result, err error) {
        // Release connection back to pool
        s.connection.Release(conn)
        
        // Submit results to aggregator for batch reporting
        s.manager.aggregator.SubmitTaskResult(line, "ping", result, duration)
    })
}
```

### 🔧 **Configuration Management**
The system supports dynamic configuration updates with zero-downtime reloading:
- **Hot Reload**: Configuration changes detected via Zabbix API polling
- **Version Control**: Monotonic version numbers for change tracking  
- **Event-Driven**: Subscriber pattern for configuration change notifications
- **Graceful Updates**: Existing tasks complete before applying new configurations
