package syncer

import (
	"context"
	"time"
)

// TestConfig holds configuration for tests
type TestConfig struct {
	SyncInterval    time.Duration
	TestTimeout     time.Duration
	EventTimeout    time.Duration
	DefaultInterval time.Duration
	ProxyIP         string
	TestProxyID     string
}

// DefaultTestConfig returns default test configuration
func DefaultTestConfig() TestConfig {
	return TestConfig{
		SyncInterval:    100 * time.Millisecond,
		TestTimeout:     30 * time.Second,
		EventTimeout:    2 * time.Second,
		DefaultInterval: 3 * time.Minute,
		ProxyIP:         "10.10.10.10",
		TestProxyID:     "10452",
	}
}

// TestAssertions provides common test assertions
type TestAssertions struct{}

// AssertLineEquals checks if two lines are equal (ignoring hash)
func (TestAssertions) AssertLineEquals(expected, actual Line) bool {
	return expected.ID == actual.ID &&
		expected.IP == actual.IP &&
		expected.Interval == actual.Interval &&
		expected.Router.IP == actual.Router.IP &&
		expected.Router.Username == actual.Router.Username &&
		expected.Router.Password == actual.Router.Password &&
		expected.Router.Platform == actual.Router.Platform &&
		expected.Router.Protocol == actual.Router.Protocol
}

// AssertEventValid checks if an event is valid
func (TestAssertions) AssertEventValid(event LineChangeEvent) bool {
	if event.Type < LineCreate || event.Type > LineDelete {
		return false
	}
	if event.Version <= 0 {
		return false
	}
	if event.Line.IP == "" {
		return false
	}
	return true
}

// TestHelper provides various helper functions for testing
type TestHelper struct{}

// WaitForCondition waits for a condition to be true with timeout
func (TestHelper) WaitForCondition(condition func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// CreateTestContext creates a context with timeout for tests
func (TestHelper) CreateTestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}
