package syncer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zapix"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// Test data helpers are now in test_helpers.go

// IntegrationTestSuite is the test suite for integration tests
type IntegrationTestSuite struct {
	suite.Suite
	mockClient *MockClient
	syncer     *ConfigSyncer
	ctx        context.Context
	cancel     context.CancelFunc
}

// SetupTest runs before each test
func (suite *IntegrationTestSuite) SetupTest() {
	suite.mockClient = &MockClient{}
	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), 30*time.Second)

	// Create syncer using helper function
	suite.syncer = createTestSyncer(suite.mockClient, 100*time.Millisecond)
}

// TearDownTest runs after each test
func (suite *IntegrationTestSuite) TearDownTest() {
	if suite.syncer != nil {
		suite.syncer.Stop()
	}
	suite.cancel()
}

// TestFullSyncWorkflow tests the complete sync workflow from start to finish
func (suite *IntegrationTestSuite) TestFullSyncWorkflow() {
	// Setup mock responses
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(hostResponse, nil)

	// Create subscribers before starting
	sub1 := suite.syncer.Subscribe(suite.ctx)
	sub2 := suite.syncer.Subscribe(suite.ctx)

	// Start syncer in background
	go suite.syncer.Start()

	// Wait for first sync and collect events
	events1 := suite.collectEvents(sub1, 2, 2*time.Second)
	events2 := suite.collectEvents(sub2, 2, 2*time.Second)

	// Verify both subscribers received the same events
	suite.Len(events1, 2)
	suite.Len(events2, 2)

	// All events should be LineCreate type for first sync
	for _, event := range events1 {
		suite.Equal(LineCreate, event.Type)
		suite.Equal(int64(1), event.Version)
	}

	// Verify syncer state
	suite.Equal(int64(1), suite.syncer.Version())
	lines := suite.syncer.GetLines()
	suite.Len(lines, 2)

	// Verify line contents
	for _, line := range lines {
		suite.NotEmpty(line.IP)
		suite.NotZero(line.Hash)
		suite.NotEmpty(line.Router.IP)
	}

	sub1.Close()
	sub2.Close()
	suite.mockClient.AssertExpectations(suite.T())
}

// TestSyncWithChanges tests sync behavior when configuration changes
func (suite *IntegrationTestSuite) TestSyncWithChanges() {
	// Initial sync setup
	initialHosts := []zapix.HostObject{
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
	}

	// Updated hosts (one modified, one added, original one removed)
	updatedHosts := []zapix.HostObject{
		{
			Host: "10.10.10.11", // Same host but different config
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "300"}, // Changed interval
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "root"},   // Changed username
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "secret"}, // Changed password
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
		{
			Host: "10.10.10.13", // New host
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line003"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "240"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.3"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "huawei_vrp"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "netconf"},
			},
		},
	}

	proxyResponse := createTestProxyResponse()

	// Setup mock calls
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Times(2)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(initialHosts, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(updatedHosts, nil).Once()

	sub := suite.syncer.Subscribe(suite.ctx)
	go suite.syncer.Start()

	// First sync - expect 1 create event
	firstEvents := suite.collectEvents(sub, 1, 2*time.Second)
	suite.Len(firstEvents, 1)
	suite.Equal(LineCreate, firstEvents[0].Type)

	// Trigger second sync by waiting for next interval
	time.Sleep(150 * time.Millisecond)

	// Second sync - expect update and create events
	secondEvents := suite.collectEvents(sub, 2, 2*time.Second)
	suite.Len(secondEvents, 2)

	// Analyze the events
	var updateCount, createCount int
	for _, event := range secondEvents {
		switch event.Type {
		case LineUpdate:
			updateCount++
		case LineCreate:
			createCount++
		}
	}

	suite.Equal(1, updateCount, "Should have 1 update event")
	suite.Equal(1, createCount, "Should have 1 create event")

	// Verify final state
	suite.Equal(int64(2), suite.syncer.Version())
	finalLines := suite.syncer.GetLines()
	suite.Len(finalLines, 2)

	sub.Close()
}

// TestSyncErrorHandling tests error scenarios during sync
func (suite *IntegrationTestSuite) TestSyncErrorHandling() {
	// Test proxy fetch error
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(nil, fmt.Errorf("connection timeout")).Once()

	sub := suite.syncer.Subscribe(suite.ctx)
	go suite.syncer.Start()

	// Should not receive any events due to error
	events := suite.collectEvents(sub, 0, 500*time.Millisecond)
	suite.Len(events, 0)

	// Version should still be 0
	suite.Equal(int64(0), suite.syncer.Version())

	sub.Close()
}

// TestConcurrentSubscriptions tests multiple subscribers with concurrent access
func (suite *IntegrationTestSuite) TestConcurrentSubscriptions() {
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostResponse, nil)

	// Create multiple subscribers concurrently
	numSubscribers := 10
	subscribers := make([]*Subscription, numSubscribers)
	eventChannels := make([][]LineChangeEvent, numSubscribers)

	var wg sync.WaitGroup

	// Create subscribers concurrently
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			subscribers[index] = suite.syncer.Subscribe(suite.ctx)
		}(i)
	}
	wg.Wait()

	// Mock setup for concurrent test
	concurrentProxyResponse := createTestProxyResponse()
	concurrentHostResponse := createTestHostResponse()
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(concurrentProxyResponse, nil)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(concurrentHostResponse, nil)

	// Start syncer
	go suite.syncer.Start()

	// Collect events from all subscribers concurrently
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			eventChannels[index] = suite.collectEvents(subscribers[index], 2, 3*time.Second)
		}(i)
	}
	wg.Wait()

	// Verify all subscribers received the same events
	for i := 0; i < numSubscribers; i++ {
		suite.Len(eventChannels[i], 2, "Subscriber %d should receive 2 events", i)
		for _, event := range eventChannels[i] {
			suite.Equal(LineCreate, event.Type)
			suite.Equal(int64(1), event.Version)
		}
	}

	// Clean up subscribers
	for _, sub := range subscribers {
		if sub != nil {
			sub.Close()
		}
	}
}

// TestSyncerLifecycle tests the complete lifecycle of syncer
func (suite *IntegrationTestSuite) TestSyncerLifecycle() {
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Maybe()
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostResponse, nil).Maybe()

	// Test initial state
	suite.False(suite.syncer.stopped)
	suite.Equal(int64(0), suite.syncer.Version())
	suite.Len(suite.syncer.GetLines(), 0)

	// Create subscriber
	sub := suite.syncer.Subscribe(suite.ctx)

	// Start syncer
	startTime := time.Now()
	go suite.syncer.Start()

	// Wait for at least one sync
	events := suite.collectEvents(sub, 2, 3*time.Second)
	suite.Len(events, 2)

	// Verify sync happened
	suite.True(time.Since(startTime) >= 100*time.Millisecond)
	suite.Equal(int64(1), suite.syncer.Version())
	suite.Len(suite.syncer.GetLines(), 2)

	// Stop syncer
	suite.syncer.Stop()

	// Verify stopped state
	suite.True(suite.syncer.stopped)

	// Subscriber channel should be closed
	select {
	case _, ok := <-sub.Events():
		suite.False(ok, "Channel should be closed after stop")
	case <-time.After(100 * time.Millisecond):
		suite.Fail("Channel should be closed immediately")
	}
}

// TestSubscriptionContextCancellation tests subscription behavior with context cancellation
func (suite *IntegrationTestSuite) TestSubscriptionContextCancellation() {
	// Create subscription with short-lived context
	subCtx, subCancel := context.WithTimeout(suite.ctx, 200*time.Millisecond)
	defer subCancel()

	_ = suite.syncer.Subscribe(subCtx)

	// Wait for context to expire
	<-subCtx.Done()

	// Give some time for cleanup
	time.Sleep(50 * time.Millisecond)

	// Check that subscriber was removed from syncer
	suite.syncer.mu.RLock()
	subscriberCount := len(suite.syncer.subscribers)
	suite.syncer.mu.RUnlock()

	suite.Equal(0, subscriberCount, "Subscription should be automatically cleaned up")
}

// TestSyncPerformance tests sync performance with larger datasets
func (suite *IntegrationTestSuite) TestSyncPerformance() {
	// Create larger dataset
	numHosts := 100
	hosts := createLargeTestHostResponse(numHosts)

	proxyResponse := createTestProxyResponse()
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(hosts, nil)

	sub := suite.syncer.Subscribe(suite.ctx)

	startTime := time.Now()
	go suite.syncer.Start()

	// Collect all events
	events := suite.collectEvents(sub, numHosts, 10*time.Second)
	syncDuration := time.Since(startTime)

	// Verify performance
	suite.Len(events, numHosts)
	suite.Less(syncDuration, 5*time.Second, "Sync should complete within 5 seconds")

	// Verify all events are correct
	for _, event := range events {
		suite.Equal(LineCreate, event.Type)
		suite.Equal(int64(1), event.Version)
		suite.NotEmpty(event.Line.IP)
	}

	sub.Close()
}

// Helper method to collect events from a subscription
func (suite *IntegrationTestSuite) collectEvents(sub *Subscription, expectedCount int, timeout time.Duration) []LineChangeEvent {
	events := make([]LineChangeEvent, 0, expectedCount)
	timeoutCh := time.After(timeout)

	for len(events) < expectedCount {
		select {
		case event := <-sub.Events():
			events = append(events, event)
		case <-timeoutCh:
			return events
		case <-suite.ctx.Done():
			return events
		}
	}

	return events
}

// TestLongRunningIntegration tests syncer behavior over extended periods
func (suite *IntegrationTestSuite) TestLongRunningIntegration() {
	if testing.Short() {
		suite.T().Skip("Skipping long-running integration test in short mode")
	}

	// Setup mock for long-running test
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(hostResponse, nil)

	// Create multiple subscribers
	const numSubscribers = 5
	subscribers := make([]*Subscription, numSubscribers)
	eventCounters := make([]int, numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		subscribers[i] = suite.syncer.Subscribe(suite.ctx)
	}

	// Start event collectors
	var wg sync.WaitGroup
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(subIndex int) {
			defer wg.Done()
			timeout := time.After(10 * time.Second)
			for {
				select {
				case <-subscribers[subIndex].Events():
					eventCounters[subIndex]++
				case <-timeout:
					return
				case <-suite.ctx.Done():
					return
				}
			}
		}(i)
	}

	// Start syncer and let it run
	go suite.syncer.Start()

	// Let it run for extended period
	time.Sleep(3 * time.Second)

	// Stop syncer and wait for cleanup
	suite.syncer.Stop()
	wg.Wait()

	// Verify long-running stability
	totalEvents := 0
	for i, count := range eventCounters {
		suite.T().Logf("Subscriber %d received %d events", i, count)
		totalEvents += count
	}

	suite.Greater(totalEvents, 0, "Should receive events during long run")
	suite.Greater(suite.syncer.Version(), int64(0), "Version should be incremented")

	// Clean up
	for _, sub := range subscribers {
		sub.Close()
	}
}

// TestRealWorldScenario tests a realistic scenario with configuration changes
func (suite *IntegrationTestSuite) TestRealWorldScenario() {
	// Simulate a real-world scenario with multiple configuration changes
	proxyResponse := createTestProxyResponse()

	// Initial configuration - 3 lines
	initialHosts := []zapix.HostObject{
		createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "pass1", "cisco_iosxe", "ssh", 180),
		createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "admin", "pass2", "huawei_vrp", "netconf", 300),
		createTestHostWithMacros("10.10.10.13", "line003", "192.168.1.3", "root", "pass3", "cisco_iosxr", "ssh", 240),
	}

	// Configuration after maintenance - 1 removed, 1 modified, 1 added
	afterMaintenanceHosts := []zapix.HostObject{
		createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "pass1", "cisco_iosxe", "ssh", 180),      // Unchanged
		createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "root", "newpass2", "huawei_vrp", "netconf", 300), // Modified auth
		// line003 removed
		createTestHostWithMacros("10.10.10.14", "line004", "192.168.1.4", "admin", "pass4", "juniper_junos", "netconf", 360), // New line
	}

	// Final configuration - scaling up
	finalHosts := make([]zapix.HostObject, 10)
	for i := 0; i < 10; i++ {
		finalHosts[i] = createTestHostWithMacros(
			fmt.Sprintf("10.10.10.%d", 11+i),
			fmt.Sprintf("line%03d", i+1),
			fmt.Sprintf("192.168.1.%d", i+1),
			"admin",
			fmt.Sprintf("pass%d", i+1),
			"cisco_iosxe",
			"ssh",
			180+(i*60), // Varying intervals
		)
	}

	// Setup mock calls in sequence
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Times(3)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(initialHosts, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(afterMaintenanceHosts, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(finalHosts, nil).Once()

	// Create subscriber to track all changes
	sub := suite.syncer.Subscribe(suite.ctx)
	defer sub.Close()

	// Scenario 1: Initial deployment
	suite.T().Log("Phase 1: Initial deployment")
	err := suite.syncer.sync()
	suite.NoError(err)

	initialEvents := suite.collectEvents(sub, 3, 2*time.Second)
	suite.Len(initialEvents, 3, "Should create 3 initial lines")
	suite.Equal(int64(1), suite.syncer.Version())

	// Scenario 2: Maintenance window with changes
	suite.T().Log("Phase 2: Maintenance window changes")
	err = suite.syncer.sync()
	suite.NoError(err)

	maintenanceEvents := suite.collectEvents(sub, 3, 2*time.Second) // 1 delete, 1 update, 1 create
	suite.Len(maintenanceEvents, 3, "Should have maintenance changes")
	suite.Equal(int64(2), suite.syncer.Version())

	// Analyze maintenance events
	eventTypes := make(map[ChangeType]int)
	for _, event := range maintenanceEvents {
		eventTypes[event.Type]++
	}
	suite.Equal(1, eventTypes[LineDelete], "Should have 1 delete")
	suite.Equal(1, eventTypes[LineUpdate], "Should have 1 update")
	suite.Equal(1, eventTypes[LineCreate], "Should have 1 create")

	// Scenario 3: Scaling up
	suite.T().Log("Phase 3: Scaling up to 10 lines")
	err = suite.syncer.sync()
	suite.NoError(err)

	// Should have many creates (new lines) and some deletes (old lines not in final config)
	scalingEvents := suite.collectEvents(sub, 10, 3*time.Second)
	suite.Greater(len(scalingEvents), 0, "Should have scaling events")
	suite.Equal(int64(3), suite.syncer.Version())

	// Final state verification
	finalLines := suite.syncer.GetLines()
	suite.Len(finalLines, 10, "Should have 10 lines in final state")

	// Verify all lines have expected characteristics
	for _, line := range finalLines {
		suite.NotEmpty(line.ID, "Line ID should not be empty")
		suite.NotEmpty(line.IP, "Line IP should not be empty")
		suite.NotZero(line.Hash, "Line hash should be computed")
		suite.NotEmpty(line.Router.IP, "Router IP should not be empty")
		suite.Positive(line.Interval, "Interval should be positive")
	}

	suite.T().Log("Real-world scenario completed successfully")
	suite.mockClient.AssertExpectations(suite.T())
}

// TestFailureRecoveryIntegration tests recovery from various failure scenarios
func (suite *IntegrationTestSuite) TestFailureRecoveryIntegration() {
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	// Phase 1: Normal operation
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(hostResponse, nil).Once()

	sub := suite.syncer.Subscribe(suite.ctx)
	defer sub.Close()

	// Successful sync
	err := suite.syncer.sync()
	suite.NoError(err)

	successEvents := suite.collectEvents(sub, 2, 2*time.Second)
	suite.Len(successEvents, 2, "Should receive success events")
	initialVersion := suite.syncer.Version()
	initialLines := suite.syncer.GetLines()

	// Phase 2: Proxy failure
	suite.T().Log("Testing proxy failure recovery")
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(nil, fmt.Errorf("proxy connection failed")).Once()

	err = suite.syncer.sync()
	suite.Error(err, "Should fail during proxy error")
	suite.Equal(initialVersion, suite.syncer.Version(), "Version should not change on error")
	suite.Equal(len(initialLines), len(suite.syncer.GetLines()), "Line count should not change on error")

	// Phase 3: Host query failure
	suite.T().Log("Testing host query failure recovery")
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(nil, fmt.Errorf("host query failed")).Once()

	err = suite.syncer.sync()
	suite.Error(err, "Should fail during host query error")
	suite.Equal(initialVersion, suite.syncer.Version(), "Version should not change on error")

	// Phase 4: Recovery
	suite.T().Log("Testing successful recovery")
	modifiedHosts := []zapix.HostObject{
		{
			Host: "10.10.10.11",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "240"}, // Changed interval
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
	}

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Once()
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(modifiedHosts, nil).Once()

	err = suite.syncer.sync()
	suite.NoError(err, "Should recover successfully")

	recoveryEvents := suite.collectEvents(sub, 2, 2*time.Second) // 1 update, 1 delete
	suite.Len(recoveryEvents, 2, "Should have recovery events")
	suite.Greater(suite.syncer.Version(), initialVersion, "Version should increment after recovery")

	// Verify recovered state
	recoveredLines := suite.syncer.GetLines()
	suite.Len(recoveredLines, 1, "Should have 1 line after recovery")

	for _, line := range recoveredLines {
		suite.Equal(4*time.Minute, line.Interval, "Should have updated interval")
	}

	suite.T().Log("Failure recovery integration test completed")
	suite.mockClient.AssertExpectations(suite.T())
}

// TestConfigurationDriftDetection tests detection of configuration drift
func (suite *IntegrationTestSuite) TestConfigurationDriftDetection() {
	proxyResponse := createTestProxyResponse()

	// Create a baseline configuration
	baselineHosts := []zapix.HostObject{
		createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "baseline", "cisco_iosxe", "ssh", 180),
		createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "admin", "baseline", "huawei_vrp", "netconf", 300),
		createTestHostWithMacros("10.10.10.13", "line003", "192.168.1.3", "admin", "baseline", "cisco_iosxr", "ssh", 240),
	}

	// Simulate configuration drift scenarios
	driftScenarios := []struct {
		name        string
		hosts       []zapix.HostObject
		description string
	}{
		{
			name: "password_drift",
			hosts: []zapix.HostObject{
				createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "changed!", "cisco_iosxe", "ssh", 180),
				createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "admin", "baseline", "huawei_vrp", "netconf", 300),
				createTestHostWithMacros("10.10.10.13", "line003", "192.168.1.3", "admin", "baseline", "cisco_iosxr", "ssh", 240),
			},
			description: "Password change in one line",
		},
		{
			name: "interval_drift",
			hosts: []zapix.HostObject{
				createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "changed!", "cisco_iosxe", "ssh", 180),
				createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "admin", "baseline", "huawei_vrp", "netconf", 600), // Changed
				createTestHostWithMacros("10.10.10.13", "line003", "192.168.1.3", "admin", "baseline", "cisco_iosxr", "ssh", 240),
			},
			description: "Interval change in another line",
		},
		{
			name: "platform_drift",
			hosts: []zapix.HostObject{
				createTestHostWithMacros("10.10.10.11", "line001", "192.168.1.1", "admin", "changed!", "cisco_iosxe", "ssh", 180),
				createTestHostWithMacros("10.10.10.12", "line002", "192.168.1.2", "admin", "baseline", "huawei_vrp", "netconf", 600),
				createTestHostWithMacros("10.10.10.13", "line003", "192.168.1.3", "admin", "baseline", "juniper_junos", "netconf", 240), // Changed
			},
			description: "Platform change in third line",
		},
	}

	// Setup baseline
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil).Times(len(driftScenarios) + 1)
	suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(baselineHosts, nil).Once()

	sub := suite.syncer.Subscribe(suite.ctx)
	defer sub.Close()

	// Establish baseline
	err := suite.syncer.sync()
	suite.NoError(err)

	baselineEvents := suite.collectEvents(sub, 3, 2*time.Second)
	suite.Len(baselineEvents, 3, "Should establish baseline")
	baselineVersion := suite.syncer.Version()

	// Test each drift scenario
	for _, scenario := range driftScenarios {
		suite.T().Logf("Testing drift scenario: %s", scenario.description)

		suite.mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
			return len(params.ProxyIDs) > 0
		})).Return(scenario.hosts, nil).Once()

		err = suite.syncer.sync()
		suite.NoError(err)

		// Should detect drift and generate update events
		driftEvents := suite.collectEvents(sub, 3, 2*time.Second)
		suite.GreaterOrEqual(len(driftEvents), 1, "Should detect configuration drift in %s", scenario.name)

		// Version should increment due to changes
		suite.Greater(suite.syncer.Version(), baselineVersion, "Version should increment for %s", scenario.name)
		baselineVersion = suite.syncer.Version()

		// Verify the changes are reflected in current state
		currentLines := suite.syncer.GetLines()
		suite.Len(currentLines, 3, "Should maintain same number of lines")

		suite.T().Logf("Detected %d drift events for %s", len(driftEvents), scenario.name)
	}

	suite.T().Log("Configuration drift detection test completed")
	suite.mockClient.AssertExpectations(suite.T())
}

// TestIntegrationSuite runs the integration test suite
func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

// Benchmark tests for performance evaluation
func BenchmarkSyncPerformance(b *testing.B) {
	mockClient := &MockClient{}

	// Create test data
	proxyResponse := createTestProxyResponse()
	hostResponse := createTestHostResponse()

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(hostResponse, nil)

	syncer := createTestSyncer(mockClient, time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := syncer.sync()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventNotification(b *testing.B) {
	syncer := createTestSyncer(&MockClient{}, time.Minute)

	// Add some subscribers
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		syncer.Subscribe(ctx)
	}

	// Create test event
	event := LineChangeEvent{
		Type: LineCreate,
		Line: createTestLine("1", "10.1.1.1", 3*time.Minute),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		syncer.notifyAll([]LineChangeEvent{event})
	}
}

func BenchmarkChangeDetection(b *testing.B) {
	syncer := createTestSyncer(&MockClient{}, time.Minute)

	// Setup initial state
	initialLines := make(map[string]Line)
	for i := 0; i < 100; i++ {
		line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.1.%d", i), 3*time.Minute)
		initialLines[line.IP] = line
	}
	syncer.lines = initialLines

	// Create modified lines
	newLines := make(map[string]Line)
	for i := 0; i < 100; i++ {
		if i%2 == 0 {
			// Keep half unchanged
			line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.1.%d", i), 3*time.Minute)
			newLines[line.IP] = line
		} else {
			// Modify half
			line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.1.%d", i), 5*time.Minute)
			newLines[line.IP] = line
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events := syncer.detectChanges(newLines)
		_ = events
	}
}
