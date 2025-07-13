# Zabbix DDL Monitor

A Go-based service to monitor network line connectivity via Zabbix, leveraging `scrapligo` for router interactions.

## Features
- Dynamic line list management via Zabbix API.
- Router connection pooling for efficiency.
- Extensible task system for platform-specific commands.
- Batch processing and result aggregation.

## Usage
1. Configure Zabbix API credentials in `config/manager.go`.
2. Define tasks in `internal/task/`.
3. Run: `go run cmd/monitor/main.go`.

