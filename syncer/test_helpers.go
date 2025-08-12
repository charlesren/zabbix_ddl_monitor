package syncer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zapix"
)

// createTestSyncer creates a ConfigSyncer instance for testing with the given client and interval
// This is the most commonly used helper function across all test files
func createTestSyncer(client Client, interval time.Duration) *ConfigSyncer {
	ctx, cancel := context.WithCancel(context.Background())
	syncer := &ConfigSyncer{
		client:       client,
		lines:        make(map[string]Line),
		syncInterval: interval,
		subscribers:  make([]chan<- LineChangeEvent, 0),
		mu:           sync.RWMutex{},
		stopOnce:     sync.Once{},
		ctx:          ctx,
		cancel:       cancel,
	}
	return syncer
}

// createTestLine creates a Line object with specified ID, IP, and interval for testing
// Used frequently in concurrent and performance tests
func createTestLine(id, ip string, interval time.Duration) Line {
	line := Line{
		ID:       id,
		IP:       ip,
		Interval: interval,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}
	line.ComputeHash()
	return line
}

// createTestProxyResponse creates a standard proxy response for mocking Zabbix API calls
// Returns a proxy with ID "10452" and host "10.10.10.10"
func createTestProxyResponse() []zapix.ProxyObject {
	return []zapix.ProxyObject{
		{
			Proxyid: "10452",
			Host:    "10.10.10.10",
		},
	}
}

// createTestHostResponse creates a standard host response with 2 test hosts for mocking
// Each host includes all required macros for line configuration
func createTestHostResponse() []zapix.HostObject {
	return []zapix.HostObject{
		{
			Host: "10.10.10.11",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
		{
			Host: "10.10.10.12",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line002"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "300"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.2"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "root"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "secret"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "huawei_vrp"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "netconf"},
			},
		},
	}
}

// createLargeTestHostResponse creates a large host response with specified count for performance testing
// Each host has unique IP and line configuration for scalability testing
func createLargeTestHostResponse(count int) []zapix.HostObject {
	hosts := make([]zapix.HostObject, count)
	for i := 0; i < count; i++ {
		hosts[i] = zapix.HostObject{
			Host: fmt.Sprintf("10.10.10.%d", i+10),
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: fmt.Sprintf("line%03d", i+1)},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
				{Macro: "{$LINE_ROUTER_IP}", Value: fmt.Sprintf("192.168.1.%d", i+1)},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		}
	}
	return hosts
}

// createEmptyProxyResponse creates an empty proxy response for error testing scenarios
func createEmptyProxyResponse() []zapix.ProxyObject {
	return []zapix.ProxyObject{}
}

// createEmptyHostResponse creates an empty host response for error testing scenarios
func createEmptyHostResponse() []zapix.HostObject {
	return []zapix.HostObject{}
}

// createTestHostWithMacros creates a host object with custom macro values for complex testing scenarios
// Used extensively in integration tests for real-world scenario simulation
func createTestHostWithMacros(host, lineID, routerIP, username, password, platform, protocol string, interval int) zapix.HostObject {
	return zapix.HostObject{
		Host: host,
		Macros: []zapix.UsermacroObject{
			{Macro: "{$LINE_ID}", Value: lineID},
			{Macro: "{$LINE_CHECK_INTERVAL}", Value: fmt.Sprintf("%d", interval)},
			{Macro: "{$LINE_ROUTER_IP}", Value: routerIP},
			{Macro: "{$LINE_ROUTER_USERNAME}", Value: username},
			{Macro: "{$LINE_ROUTER_PASSWORD}", Value: password},
			{Macro: "{$LINE_ROUTER_PLATFORM}", Value: platform},
			{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: protocol},
		},
	}
}
