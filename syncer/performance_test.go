package syncer

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// PerformanceTestSuite provides performance and benchmark tests
type PerformanceTestSuite struct {
	suite.Suite
	mockClient *MockClient
	syncer     *ConfigSyncer
	ctx        context.Context
	cancel     context.CancelFunc
}

func (suite *PerformanceTestSuite) SetupTest() {
	suite.mockClient = &MockClient{}
	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), 60*time.Second)
	suite.syncer = createTestSyncer(suite.mockClient, time.Second)
}

func (suite *PerformanceTestSuite) TearDownTest() {
	if suite.syncer != nil {
		suite.syncer.Stop()
	}
	suite.cancel()
}

// TestLargeDatasetSync tests performance with large datasets
func (suite *PerformanceTestSuite) TestLargeDatasetSync() {
	testSizes := []int{100, 500, 1000, 2000}

	for _, size := range testSizes {
		suite.T().Run(fmt.Sprintf("dataset_size_%d", size), func(t *testing.T) {
			// Create large dataset
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			proxyResponse := createTestProxyResponse()
			hostResponse := createLargeTestHostResponse(size)

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
			mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostResponse, nil)

			// Measure sync time
			start := time.Now()
			err := syncer.sync()
			duration := time.Since(start)

			assert.NoError(t, err)
			assert.Equal(t, int64(1), syncer.Version())

			lines := syncer.GetLines()
			assert.Len(t, lines, size)

			t.Logf("Dataset size: %d, Sync duration: %v, Lines per second: %.2f",
				size, duration, float64(size)/duration.Seconds())

			// Performance expectations (adjust based on requirements)
			maxExpectedDuration := time.Duration(size) * time.Millisecond // 1ms per line max
			assert.Less(t, duration, maxExpectedDuration,
				"Sync should complete within reasonable time for size %d", size)
		})
	}
}

// TestMemoryUsageWithLargeDataset tests memory usage with large datasets
func (suite *PerformanceTestSuite) TestMemoryUsageWithLargeDataset() {
	// Force GC to get baseline
	runtime.GC()
	var beforeStats runtime.MemStats
	runtime.ReadMemStats(&beforeStats)

	const datasetSize = 5000
	proxyResponse := createTestProxyResponse()
	hostResponse := createLargeTestHostResponse(datasetSize)

	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(proxyResponse, nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostResponse, nil)

	// Perform sync
	err := suite.syncer.sync()
	suite.NoError(err)

	// Measure memory after sync
	runtime.GC()
	var afterStats runtime.MemStats
	runtime.ReadMemStats(&afterStats)

	memoryUsed := afterStats.Alloc - beforeStats.Alloc
	memoryPerLine := float64(memoryUsed) / float64(datasetSize)

	suite.T().Logf("Dataset size: %d lines", datasetSize)
	suite.T().Logf("Memory used: %d bytes", memoryUsed)
	suite.T().Logf("Memory per line: %.2f bytes", memoryPerLine)

	// Memory usage should be reasonable (adjust based on data structure size)
	const maxMemoryPerLine = 3072 // 3KB per line (includes Go runtime overhead)
	suite.Less(memoryPerLine, float64(maxMemoryPerLine),
		"Memory usage per line should be reasonable")
}

// TestHighFrequencySync tests performance with high frequency sync operations
func (suite *PerformanceTestSuite) TestHighFrequencySync() {
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	const numSyncs = 100
	const syncInterval = 10 * time.Millisecond

	start := time.Now()
	for i := 0; i < numSyncs; i++ {
		err := suite.syncer.sync()
		suite.NoError(err, "Sync %d should succeed", i)

		if i < numSyncs-1 {
			time.Sleep(syncInterval)
		}
	}
	totalDuration := time.Since(start)

	expectedMinDuration := time.Duration(numSyncs-1) * syncInterval
	suite.GreaterOrEqual(totalDuration, expectedMinDuration,
		"Total duration should account for sleep intervals")

	avgSyncDuration := (totalDuration - expectedMinDuration) / numSyncs
	suite.T().Logf("Average sync duration: %v", avgSyncDuration)

	// High frequency syncs should complete quickly
	const maxAvgSyncDuration = 50 * time.Millisecond
	suite.Less(avgSyncDuration, maxAvgSyncDuration,
		"Average sync duration should be reasonable for high frequency")
}

// TestManySubscribersPerformance tests performance with many concurrent subscribers
func (suite *PerformanceTestSuite) TestManySubscribersPerformance() {
	const numSubscribers = 1000
	const numEvents = 50

	// Create many subscribers
	subscribers := make([]*Subscription, numSubscribers)
	eventCounts := make([]int, numSubscribers)
	var wg sync.WaitGroup

	for i := 0; i < numSubscribers; i++ {
		subscribers[i] = suite.syncer.Subscribe(suite.ctx)
	}

	// Start event collectors
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(subIndex int) {
			defer wg.Done()
			timeout := time.After(10 * time.Second)
			for {
				select {
				case <-subscribers[subIndex].Events():
					eventCounts[subIndex]++
					if eventCounts[subIndex] >= numEvents {
						return
					}
				case <-timeout:
					return
				case <-suite.ctx.Done():
					return
				}
			}
		}(i)
	}

	// Measure event notification performance
	start := time.Now()
	for i := 0; i < numEvents; i++ {
		event := LineChangeEvent{
			Type:    LineCreate,
			Line:    createTestLine(fmt.Sprintf("perf-line%d", i), fmt.Sprintf("10.1.1.%d", i%255), 3*time.Minute),
			Version: int64(i + 1),
		}
		suite.syncer.notifyAll([]LineChangeEvent{event})
	}

	wg.Wait()
	notificationDuration := time.Since(start)

	// Calculate statistics
	totalEventsDelivered := 0
	for _, count := range eventCounts {
		totalEventsDelivered += count
	}

	expectedTotalEvents := numSubscribers * numEvents
	deliveryRate := float64(totalEventsDelivered) / float64(expectedTotalEvents) * 100

	suite.T().Logf("Subscribers: %d", numSubscribers)
	suite.T().Logf("Events per subscriber: %d", numEvents)
	suite.T().Logf("Total events sent: %d", numEvents)
	suite.T().Logf("Total events delivered: %d", totalEventsDelivered)
	suite.T().Logf("Delivery rate: %.2f%%", deliveryRate)
	suite.T().Logf("Notification duration: %v", notificationDuration)
	suite.T().Logf("Events per second: %.2f", float64(totalEventsDelivered)/notificationDuration.Seconds())

	// Performance expectations
	suite.Greater(deliveryRate, 90.0, "Event delivery rate should be high")

	// Clean up
	for _, sub := range subscribers {
		sub.Close()
	}
}

// TestChangeDetectionPerformance tests performance of change detection algorithm
func (suite *PerformanceTestSuite) TestChangeDetectionPerformance() {
	testSizes := []int{100, 500, 1000, 2000}

	for _, size := range testSizes {
		suite.T().Run(fmt.Sprintf("change_detection_size_%d", size), func(t *testing.T) {
			syncer := createTestSyncer(&MockClient{}, time.Minute)

			// Create initial dataset
			initialLines := make(map[string]Line, size)
			for i := 0; i < size; i++ {
				line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 3*time.Minute)
				initialLines[line.IP] = line
			}
			syncer.lines = initialLines

			// Create modified dataset (50% unchanged, 25% modified, 25% new, remove 25% old)
			newLines := make(map[string]Line, size)

			// Keep 50% unchanged
			for i := 0; i < size/2; i++ {
				ip := fmt.Sprintf("10.1.%d.%d", i/255, i%255)
				newLines[ip] = initialLines[ip]
			}

			// Modify 25%
			for i := size / 2; i < 3*size/4; i++ {
				line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 5*time.Minute) // Different interval
				newLines[line.IP] = line
			}

			// Add 25% new
			for i := size; i < 5*size/4; i++ {
				line := createTestLine(fmt.Sprintf("new-line%d", i), fmt.Sprintf("10.2.%d.%d", i/255, i%255), 3*time.Minute)
				newLines[line.IP] = line
			}

			// Measure change detection performance
			start := time.Now()
			changes := syncer.detectChanges(newLines)
			duration := time.Since(start)

			// Analyze results
			createCount := 0
			updateCount := 0
			deleteCount := 0
			for _, change := range changes {
				switch change.Type {
				case LineCreate:
					createCount++
				case LineUpdate:
					updateCount++
				case LineDelete:
					deleteCount++
				}
			}

			t.Logf("Dataset size: %d", size)
			t.Logf("Change detection duration: %v", duration)
			t.Logf("Creates: %d, Updates: %d, Deletes: %d", createCount, updateCount, deleteCount)
			t.Logf("Changes per second: %.2f", float64(len(changes))/duration.Seconds())

			// Verify expected changes
			assert.Equal(t, size/4, createCount, "Should detect correct number of creates")
			assert.Equal(t, size/4, updateCount, "Should detect correct number of updates")
			assert.Equal(t, size/4, deleteCount, "Should detect correct number of deletes")

			// Performance expectation
			maxExpectedDuration := time.Duration(size) * 100 * time.Microsecond // 100μs per item
			assert.Less(t, duration, maxExpectedDuration,
				"Change detection should complete within reasonable time for size %d", size)
		})
	}
}

// TestSubscriptionScalability tests scalability of subscription system
func (suite *PerformanceTestSuite) TestSubscriptionScalability() {
	subscriberCounts := []int{10, 50, 100, 500, 1000}

	for _, count := range subscriberCounts {
		suite.T().Run(fmt.Sprintf("subscribers_%d", count), func(t *testing.T) {
			syncer := createTestSyncer(&MockClient{}, time.Minute)

			// Create subscribers
			start := time.Now()
			subscribers := make([]*Subscription, count)
			for i := 0; i < count; i++ {
				subscribers[i] = syncer.Subscribe(suite.ctx)
			}
			subscriptionTime := time.Since(start)

			// Test event notification performance
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine("scalability-test", "10.1.1.1", 3*time.Minute),
				Version: 1,
			}

			start = time.Now()
			syncer.notifyAll([]LineChangeEvent{event})
			notificationTime := time.Since(start)

			// Clean up
			start = time.Now()
			for _, sub := range subscribers {
				sub.Close()
			}
			cleanupTime := time.Since(start)

			t.Logf("Subscribers: %d", count)
			t.Logf("Subscription creation time: %v (%.2fμs per subscription)",
				subscriptionTime, float64(subscriptionTime.Nanoseconds())/float64(count)/1000)
			t.Logf("Event notification time: %v", notificationTime)
			t.Logf("Cleanup time: %v (%.2fμs per subscription)",
				cleanupTime, float64(cleanupTime.Nanoseconds())/float64(count)/1000)

			// Performance expectations
			maxSubscriptionTime := time.Duration(count) * 100 * time.Microsecond // 100μs per subscription
			assert.Less(t, subscriptionTime, maxSubscriptionTime,
				"Subscription creation should scale linearly")

			maxNotificationTime := time.Duration(count) * 10 * time.Microsecond // 10μs per notification
			assert.Less(t, notificationTime, maxNotificationTime,
				"Event notification should scale linearly")
		})
	}
}

func TestPerformanceSuite(t *testing.T) {
	suite.Run(t, new(PerformanceTestSuite))
}

// Benchmark tests for detailed performance measurement

func BenchmarkSyncPerformanceBySize(b *testing.B) {
	sizes := []int{10, 50, 100, 500, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			mockClient := &MockClient{}
			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
			mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createLargeTestHostResponse(size), nil)

			syncer := createTestSyncer(mockClient, time.Minute)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				err := syncer.sync()
				if err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(size), "lines")
		})
	}
}

func BenchmarkChangeDetectionBySize(b *testing.B) {
	sizes := []int{100, 500, 1000, 2000, 5000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			syncer := createTestSyncer(&MockClient{}, time.Minute)

			// Setup initial data
			initialLines := make(map[string]Line, size)
			for i := 0; i < size; i++ {
				line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 3*time.Minute)
				initialLines[line.IP] = line
			}
			syncer.lines = initialLines

			// Create modified lines (modify 50% of them)
			newLines := make(map[string]Line, size)
			for i := 0; i < size; i++ {
				if i%2 == 0 {
					// Keep unchanged
					newLines[initialLines[fmt.Sprintf("10.1.%d.%d", i/255, i%255)].IP] = initialLines[fmt.Sprintf("10.1.%d.%d", i/255, i%255)]
				} else {
					// Modify
					line := createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 5*time.Minute)
					newLines[line.IP] = line
				}
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				changes := syncer.detectChanges(newLines)
				_ = changes // Prevent optimization
			}

			b.ReportMetric(float64(size), "lines")
		})
	}
}

func BenchmarkEventNotificationBySubscribers(b *testing.B) {
	subscriberCounts := []int{1, 10, 50, 100, 500, 1000}

	for _, count := range subscriberCounts {
		b.Run(fmt.Sprintf("subscribers_%d", count), func(b *testing.B) {
			syncer := createTestSyncer(&MockClient{}, time.Minute)
			ctx := context.Background()

			// Create subscribers
			subscribers := make([]*Subscription, count)
			for i := 0; i < count; i++ {
				subscribers[i] = syncer.Subscribe(ctx)
			}

			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine("benchmark", "10.1.1.1", 3*time.Minute),
				Version: 1,
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				syncer.notifyAll([]LineChangeEvent{event})
			}

			// Clean up
			for _, sub := range subscribers {
				sub.Close()
			}

			b.ReportMetric(float64(count), "subscribers")
		})
	}
}

func BenchmarkLineHashComputation(b *testing.B) {
	line := createTestLine("benchmark-line", "10.1.1.1", 3*time.Minute)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		line.ComputeHash()
	}
}

func BenchmarkSubscriptionCreationAndDestruction(b *testing.B) {
	syncer := createTestSyncer(&MockClient{}, time.Minute)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sub := syncer.Subscribe(ctx)
		sub.Close()
	}
}

func BenchmarkConcurrentMixedOperations(b *testing.B) {
	mockClient := &MockClient{}
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	syncer := createTestSyncer(mockClient, time.Minute)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Mix of operations
			switch b.N % 4 {
			case 0:
				syncer.sync()
			case 1:
				_ = syncer.Version()
			case 2:
				_ = syncer.GetLines()
			case 3:
				sub := syncer.Subscribe(context.Background())
				sub.Close()
			}
		}
	})
}

// Memory allocation benchmarks

func BenchmarkMemoryAllocations(b *testing.B) {
	b.Run("LineCreation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.1.%d", i%255), 3*time.Minute)
		}
	})

	b.Run("EventCreation", func(b *testing.B) {
		line := createTestLine("test", "10.1.1.1", 3*time.Minute)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = LineChangeEvent{
				Type:    LineCreate,
				Line:    line,
				Version: int64(i),
			}
		}
	})

	b.Run("MapOperations", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			lines := make(map[string]Line, 100)
			for j := 0; j < 100; j++ {
				line := createTestLine(fmt.Sprintf("line%d", j), fmt.Sprintf("10.1.1.%d", j), 3*time.Minute)
				lines[line.IP] = line
			}
		}
	})
}

// Stress tests for performance under load

func TestStressSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	mockClient := &MockClient{}
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createLargeTestHostResponse(10000), nil)

	syncer := createTestSyncer(mockClient, time.Minute)

	const numIterations = 100
	start := time.Now()

	for i := 0; i < numIterations; i++ {
		err := syncer.sync()
		assert.NoError(t, err, "Stress sync iteration %d should succeed", i)

		if i%10 == 0 {
			t.Logf("Completed %d/%d stress sync iterations", i+1, numIterations)
		}
	}

	totalDuration := time.Since(start)
	avgDuration := totalDuration / numIterations

	t.Logf("Stress test completed: %d iterations in %v (avg: %v per iteration)",
		numIterations, totalDuration, avgDuration)

	// Verify final state
	assert.Equal(t, int64(numIterations), syncer.Version())
	assert.Len(t, syncer.GetLines(), 10000)
}

func TestStressSubscriptions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	syncer := createTestSyncer(&MockClient{}, time.Minute)
	ctx := context.Background()

	const numCycles = 50
	const subscribersPerCycle = 100

	for cycle := 0; cycle < numCycles; cycle++ {
		// Create subscribers
		subscribers := make([]*Subscription, subscribersPerCycle)
		for i := 0; i < subscribersPerCycle; i++ {
			subscribers[i] = syncer.Subscribe(ctx)
		}

		// Send events
		for i := 0; i < 10; i++ {
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine(fmt.Sprintf("stress-%d-%d", cycle, i), fmt.Sprintf("10.%d.1.%d", cycle%255, i), 3*time.Minute),
				Version: int64(cycle*10 + i),
			}
			syncer.notifyAll([]LineChangeEvent{event})
		}

		// Close subscribers
		for _, sub := range subscribers {
			sub.Close()
		}

		if cycle%10 == 0 {
			t.Logf("Completed %d/%d stress subscription cycles", cycle+1, numCycles)
		}
	}

	// Verify cleanup
	syncer.mu.RLock()
	subscriberCount := len(syncer.subscribers)
	syncer.mu.RUnlock()

	assert.Equal(t, 0, subscriberCount, "All subscribers should be cleaned up")
	t.Log("Stress subscription test completed successfully")
}

// TestStressMemoryManagement tests memory management under stress
func TestStressMemoryManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Record initial memory stats
	runtime.GC()
	var initialStats runtime.MemStats
	runtime.ReadMemStats(&initialStats)

	const numCycles = 50
	const linesPerCycle = 100

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)

	for cycle := 0; cycle < numCycles; cycle++ {
		// Create large host response
		hosts := createLargeTestHostResponse(linesPerCycle)
		mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hosts, nil).Once()

		// Perform sync
		err := syncer.sync()
		assert.NoError(t, err, "Sync should succeed in cycle %d", cycle)

		// Verify lines were created
		lines := syncer.GetLines()
		assert.Len(t, lines, linesPerCycle, "Should have correct number of lines in cycle %d", cycle)

		// Force garbage collection every 10 cycles
		if cycle%10 == 0 {
			runtime.GC()
			var midStats runtime.MemStats
			runtime.ReadMemStats(&midStats)
			t.Logf("Cycle %d: Memory used: %d KB", cycle, midStats.Alloc/1024)
		}
	}

	// Final memory check
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)

	memoryGrowth := finalStats.Alloc - initialStats.Alloc
	t.Logf("Memory growth after %d cycles: %d KB", numCycles, memoryGrowth/1024)

	// Memory growth should be reasonable (less than 50MB for this test)
	maxAcceptableGrowth := uint64(50 * 1024 * 1024) // 50MB
	assert.Less(t, memoryGrowth, maxAcceptableGrowth, "Memory growth should be within acceptable limits")

	mockClient.AssertExpectations(t)
}

// TestStressGoroutineManagement tests goroutine leak prevention
func TestStressGoroutineManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Record initial goroutine count
	initialGoroutines := runtime.NumGoroutine()

	const numIterations = 20

	for i := 0; i < numIterations; i++ {
		mockClient := &MockClient{}
		syncer := createTestSyncer(mockClient, 10*time.Millisecond)

		// Create multiple subscribers
		const numSubscribers = 10
		subscribers := make([]*Subscription, numSubscribers)
		ctx := context.Background()

		for j := 0; j < numSubscribers; j++ {
			subscribers[j] = syncer.Subscribe(ctx)
		}

		// Generate some events
		for k := 0; k < 5; k++ {
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine(fmt.Sprintf("stress-%d-%d", i, k), fmt.Sprintf("10.%d.1.%d", i%255, k), 3*time.Minute),
				Version: int64(k + 1),
			}
			syncer.notifyAll([]LineChangeEvent{event})
		}

		// Clean up
		for _, sub := range subscribers {
			sub.Close()
		}
		syncer.Stop()

		// Force cleanup
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}

	// Final goroutine count check
	finalGoroutines := runtime.NumGoroutine()
	goroutineGrowth := finalGoroutines - initialGoroutines

	t.Logf("Goroutine count: initial=%d, final=%d, growth=%d", initialGoroutines, finalGoroutines, goroutineGrowth)

	// Allow some tolerance for background goroutines
	const maxAcceptableGrowth = 10
	assert.Less(t, goroutineGrowth, maxAcceptableGrowth, "Goroutine leak detected")
}

// TestStressResourceExhaustion tests behavior under resource exhaustion
func TestStressResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Test with extremely large dataset to stress memory allocation
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	const extremeDatasetSize = 50000
	largeHosts := createLargeTestHostResponse(extremeDatasetSize)

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(largeHosts, nil)

	// Monitor memory before sync
	runtime.GC()
	var beforeStats runtime.MemStats
	runtime.ReadMemStats(&beforeStats)

	// Perform sync with large dataset
	start := time.Now()
	err := syncer.sync()
	duration := time.Since(start)

	// Should handle large dataset gracefully
	assert.NoError(t, err, "Should handle large dataset without error")

	lines := syncer.GetLines()
	assert.Len(t, lines, extremeDatasetSize, "Should process all lines")

	// Monitor memory after sync
	runtime.GC()
	var afterStats runtime.MemStats
	runtime.ReadMemStats(&afterStats)

	memoryUsed := afterStats.Alloc - beforeStats.Alloc
	memoryPerLine := float64(memoryUsed) / float64(extremeDatasetSize)

	t.Logf("Extreme dataset test:")
	t.Logf("  Dataset size: %d lines", extremeDatasetSize)
	t.Logf("  Processing time: %v", duration)
	t.Logf("  Memory used: %d MB", memoryUsed/(1024*1024))
	t.Logf("  Memory per line: %.2f bytes", memoryPerLine)

	// Performance expectations for extreme dataset
	maxExpectedDuration := 10 * time.Second
	assert.Less(t, duration, maxExpectedDuration, "Should complete within reasonable time even for extreme dataset")

	// Memory usage should be reasonable (less than 10KB per line including overhead)
	maxMemoryPerLine := float64(10240) // 10KB per line
	assert.Less(t, memoryPerLine, maxMemoryPerLine, "Memory usage per line should be reasonable")

	mockClient.AssertExpectations(t)
}

// TestStressConcurrentOperations tests concurrent operations under stress
func TestStressConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Setup successful mock responses
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createLargeTestHostResponse(1000), nil)

	const numOperations = 1000
	const numWorkers = 50

	var wg sync.WaitGroup
	operationChan := make(chan int, numOperations)

	// Fill operation channel
	for i := 0; i < numOperations; i++ {
		operationChan <- i
	}
	close(operationChan)

	// Start workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for opID := range operationChan {
				switch opID % 5 {
				case 0:
					// Sync operation
					syncer.sync()
				case 1:
					// Version read
					_ = syncer.Version()
				case 2:
					// Lines read
					_ = syncer.GetLines()
				case 3:
					// Subscription creation and cleanup
					sub := syncer.Subscribe(context.Background())
					sub.Close()
				case 4:
					// Event notification
					event := LineChangeEvent{
						Type:    LineCreate,
						Line:    createTestLine(fmt.Sprintf("stress-%d-%d", workerID, opID), fmt.Sprintf("10.1.1.%d", opID%255), 3*time.Minute),
						Version: int64(opID),
					}
					syncer.notifyAll([]LineChangeEvent{event})
				}
			}
		}(i)
	}

	// Wait for all operations to complete
	start := time.Now()
	wg.Wait()
	duration := time.Since(start)

	t.Logf("Stress concurrent operations completed:")
	t.Logf("  Operations: %d", numOperations)
	t.Logf("  Workers: %d", numWorkers)
	t.Logf("  Duration: %v", duration)
	t.Logf("  Operations per second: %.2f", float64(numOperations)/duration.Seconds())

	// System should remain stable
	assert.NotZero(t, syncer.Version(), "Syncer should have processed operations")

	// Final cleanup
	syncer.Stop()
	runtime.GC()

	// Check for goroutine leaks
	time.Sleep(100 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()
	t.Logf("Final goroutine count: %d", finalGoroutines)
}

// TestStressEventBurst tests handling of large event bursts
func TestStressEventBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	syncer := createTestSyncer(&MockClient{}, time.Minute)

	// Create many subscribers
	const numSubscribers = 100
	const burstSize = 10000

	subscribers := make([]*Subscription, numSubscribers)
	ctx := context.Background()

	for i := 0; i < numSubscribers; i++ {
		subscribers[i] = syncer.Subscribe(ctx)
	}

	// Prepare large event burst
	events := make([]LineChangeEvent, burstSize)
	for i := 0; i < burstSize; i++ {
		events[i] = LineChangeEvent{
			Type:    LineCreate,
			Line:    createTestLine(fmt.Sprintf("burst-%d", i), fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256), 3*time.Minute),
			Version: int64(i + 1),
		}
	}

	// Send burst
	start := time.Now()
	syncer.notifyAll(events)
	notificationTime := time.Since(start)

	t.Logf("Event burst test:")
	t.Logf("  Subscribers: %d", numSubscribers)
	t.Logf("  Events in burst: %d", burstSize)
	t.Logf("  Notification time: %v", notificationTime)
	t.Logf("  Events per second: %.2f", float64(burstSize)/notificationTime.Seconds())

	// Try to collect some events (not all will be received due to buffering)
	receivedCounts := make([]int, numSubscribers)
	timeout := time.After(5 * time.Second)

	done := make(chan bool, numSubscribers)
	for i := 0; i < numSubscribers; i++ {
		go func(subIndex int) {
			for {
				select {
				case <-subscribers[subIndex].Events():
					receivedCounts[subIndex]++
				case <-timeout:
					done <- true
					return
				case <-time.After(100 * time.Millisecond):
					done <- true
					return
				}
			}
		}(i)
	}

	// Wait for collection to complete
	for i := 0; i < numSubscribers; i++ {
		<-done
	}

	// Calculate statistics
	totalReceived := 0
	minReceived := burstSize
	maxReceived := 0

	for _, count := range receivedCounts {
		totalReceived += count
		if count < minReceived {
			minReceived = count
		}
		if count > maxReceived {
			maxReceived = count
		}
	}

	avgReceived := float64(totalReceived) / float64(numSubscribers)

	t.Logf("Event reception statistics:")
	t.Logf("  Total received: %d", totalReceived)
	t.Logf("  Average per subscriber: %.2f", avgReceived)
	t.Logf("  Min received: %d", minReceived)
	t.Logf("  Max received: %d", maxReceived)

	// Should receive some events (exact count depends on buffering and timing)
	assert.Greater(t, totalReceived, 0, "Should receive at least some events")
	assert.Greater(t, avgReceived, 0.0, "Average should be positive")

	// Clean up
	for _, sub := range subscribers {
		sub.Close()
	}

	t.Log("Event burst stress test completed")
}
