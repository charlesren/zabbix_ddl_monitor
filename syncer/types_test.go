package syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/stretchr/testify/assert"
)

func TestLine_ComputeHash(t *testing.T) {
	tests := []struct {
		name     string
		line1    Line
		line2    Line
		samehash bool
	}{
		{
			name: "identical lines should have same hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: true,
		},
		{
			name: "different ID should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line002", // Different ID
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different IP should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.2", // Different IP
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different interval should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 5 * time.Minute, // Different interval
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different router IP should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.2", // Different router IP
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different router username should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "root", // Different username
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different router password should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "secret", // Different password
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "different router platform should have different hash",
			line1: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			line2: Line{
				ID:       "line001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "admin",
					Password: "password",
					Platform: connection.Platform("huawei_vrp"), // Different platform
					Protocol: connection.Protocol("ssh"),
				},
			},
			samehash: false,
		},
		{
			name: "empty fields should still compute hash",
			line1: Line{
				ID:       "",
				IP:       "",
				Interval: 0,
				Router: Router{
					IP:       "",
					Username: "",
					Password: "",
					Platform: "",
					Protocol: "",
				},
			},
			line2: Line{
				ID:       "",
				IP:       "",
				Interval: 0,
				Router: Router{
					IP:       "",
					Username: "",
					Password: "",
					Platform: "",
					Protocol: "",
				},
			},
			samehash: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.line1.ComputeHash()
			tt.line2.ComputeHash()

			if tt.samehash {
				assert.Equal(t, tt.line1.Hash, tt.line2.Hash, "Expected same hash for identical lines")
			} else {
				assert.NotEqual(t, tt.line1.Hash, tt.line2.Hash, "Expected different hash for different lines")
			}

			// Hash should be non-zero (unless all fields are empty)
			if tt.line1.ID != "" || tt.line1.IP != "" || tt.line1.Interval != 0 ||
				tt.line1.Router.IP != "" || tt.line1.Router.Username != "" {
				assert.NotZero(t, tt.line1.Hash, "Hash should not be zero for non-empty lines")
			}
		})
	}
}

func TestRouter_ToConnectionConfig(t *testing.T) {
	tests := []struct {
		name   string
		router Router
	}{
		{
			name: "complete router config",
			router: Router{
				IP:       "192.168.1.1",
				Username: "admin",
				Password: "password",
				Platform: connection.Platform("cisco_iosxe"),
				Protocol: connection.Protocol("ssh"),
			},
		},
		{
			name: "minimal router config",
			router: Router{
				IP:       "10.1.1.1",
				Username: "user",
				Password: "pass",
				Platform: "",
				Protocol: "",
			},
		},
		{
			name: "empty router config",
			router: Router{
				IP:       "",
				Username: "",
				Password: "",
				Platform: "",
				Protocol: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.router.ToConnectionConfig()

			assert.Equal(t, tt.router.IP, config.IP)
			assert.Equal(t, tt.router.Username, config.Username)
			assert.Equal(t, tt.router.Password, config.Password)
			assert.NotNil(t, config.Metadata)

			// Check metadata contains platform and protocol
			platform, hasPlat := config.Metadata["platform"]
			protocol, hasProt := config.Metadata["protocol"]

			assert.True(t, hasPlat, "Metadata should contain platform")
			assert.True(t, hasProt, "Metadata should contain protocol")
			assert.Equal(t, tt.router.Platform, platform)
			assert.Equal(t, tt.router.Protocol, protocol)
		})
	}
}

func TestLineChangeEvent_Types(t *testing.T) {
	line := Line{
		ID:       "test",
		IP:       "10.1.1.1",
		Interval: time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}

	tests := []struct {
		name      string
		eventType ChangeType
		expected  ChangeType
	}{
		{
			name:      "LineCreate event",
			eventType: LineCreate,
			expected:  1,
		},
		{
			name:      "LineUpdate event",
			eventType: LineUpdate,
			expected:  2,
		},
		{
			name:      "LineDelete event",
			eventType: LineDelete,
			expected:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := LineChangeEvent{
				Type:    tt.eventType,
				Line:    line,
				Version: 1,
			}

			assert.Equal(t, tt.expected, event.Type)
			assert.Equal(t, line.IP, event.Line.IP)
			assert.Equal(t, int64(1), event.Version)
		})
	}
}

func TestFetchRouterDetails(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{
			name: "valid IP",
			ip:   "192.168.1.1",
		},
		{
			name: "empty IP",
			ip:   "",
		},
		{
			name: "localhost IP",
			ip:   "127.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, err := FetchRouterDetails(tt.ip)

			// Based on the current implementation, it always returns a router and no error
			assert.NoError(t, err)
			assert.NotNil(t, router)
			assert.Equal(t, tt.ip, router.IP)
			assert.Equal(t, "admin", router.Username)
			assert.Equal(t, "password", router.Password)
			assert.Equal(t, connection.Platform("cisco_iosxe"), router.Platform)
		})
	}
}

func TestDefaultValues(t *testing.T) {
	t.Run("DefaultInterval", func(t *testing.T) {
		assert.Equal(t, 3*time.Minute, DefaultInterval)
	})

	t.Run("ProxyIP", func(t *testing.T) {
		assert.Equal(t, "1.1.1.1", ProxyIP)
	})

	t.Run("LineSelectTag", func(t *testing.T) {
		assert.Equal(t, "TempType", LineSelectTag)
	})

	t.Run("LineSelectValue", func(t *testing.T) {
		assert.Equal(t, "ddl", LineSelectValue)
	})
}

func TestLineComputeHashConsistency(t *testing.T) {
	// Test that computing hash multiple times gives the same result
	line := Line{
		ID:       "line001",
		IP:       "10.1.1.1",
		Interval: 3 * time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}

	line.ComputeHash()
	firstHash := line.Hash

	line.ComputeHash()
	secondHash := line.Hash

	line.ComputeHash()
	thirdHash := line.Hash

	assert.Equal(t, firstHash, secondHash, "Hash should be consistent across multiple computations")
	assert.Equal(t, secondHash, thirdHash, "Hash should be consistent across multiple computations")
	assert.NotZero(t, firstHash, "Hash should not be zero")
}

func TestLineHashAfterModification(t *testing.T) {
	line := Line{
		ID:       "line001",
		IP:       "10.1.1.1",
		Interval: 3 * time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}

	line.ComputeHash()
	originalHash := line.Hash

	// Modify the line
	line.Router.Username = "root"
	line.ComputeHash()
	newHash := line.Hash

	assert.NotEqual(t, originalHash, newHash, "Hash should change after modification")
}

func BenchmarkLineComputeHash(b *testing.B) {
	line := Line{
		ID:       "line001",
		IP:       "10.1.1.1",
		Interval: 3 * time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		line.ComputeHash()
	}
}

func BenchmarkRouterToConnectionConfig(b *testing.B) {
	router := Router{
		IP:       "192.168.1.1",
		Username: "admin",
		Password: "password",
		Platform: connection.Platform("cisco_iosxe"),
		Protocol: connection.Protocol("ssh"),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = router.ToConnectionConfig()
	}
}

// TestLine_EdgeCases tests edge cases in Line struct
func TestLine_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		line Line
		desc string
	}{
		{
			name: "zero_values",
			line: Line{
				ID:       "",
				IP:       "",
				Interval: 0,
				Router: Router{
					IP:       "",
					Username: "",
					Password: "",
					Platform: "",
					Protocol: "",
				},
			},
			desc: "Line with all zero values",
		},
		{
			name: "unicode_values",
			line: Line{
				ID:       "线路001",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "管理员",
					Password: "密码123",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			desc: "Line with unicode characters",
		},
		{
			name: "special_characters",
			line: Line{
				ID:       "line-001_test@domain.com",
				IP:       "10.1.1.1",
				Interval: 3 * time.Minute,
				Router: Router{
					IP:       "192.168.1.1",
					Username: "user@domain.com",
					Password: "p@ssw0rd!#$%",
					Platform: connection.Platform("cisco_iosxe"),
					Protocol: connection.Protocol("ssh"),
				},
			},
			desc: "Line with special characters",
		},
		{
			name: "very_long_values",
			line: Line{
				ID:       string(make([]byte, 1000)), // Very long ID
				IP:       "10.1.1.1",
				Interval: 24 * time.Hour, // Very long interval
				Router: Router{
					IP:       "192.168.1.1",
					Username: string(make([]byte, 500)),
					Password: string(make([]byte, 500)),
					Platform: connection.Platform("very_long_platform_name_that_exceeds_normal_length"),
					Protocol: connection.Protocol("very_long_protocol_name"),
				},
			},
			desc: "Line with very long values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test hash computation doesn't panic
			assert.NotPanics(t, func() {
				tt.line.ComputeHash()
			}, tt.desc)

			// Test hash is computed
			assert.NotEqual(t, uint64(0), tt.line.Hash, "Hash should be non-zero for %s", tt.desc)

			// Test ToConnectionConfig doesn't panic
			assert.NotPanics(t, func() {
				config := tt.line.Router.ToConnectionConfig()
				assert.NotNil(t, config.Metadata, "Metadata should not be nil for %s", tt.desc)
			}, tt.desc)
		})
	}
}

// TestChangeType_Values tests ChangeType enum values
func TestChangeType_Values(t *testing.T) {
	tests := []struct {
		changeType ChangeType
		expected   uint8
		name       string
	}{
		{LineCreate, 1, "LineCreate"},
		{LineUpdate, 2, "LineUpdate"},
		{LineDelete, 3, "LineDelete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, uint8(tt.changeType))
		})
	}
}

// TestLineChangeEvent_Validation tests LineChangeEvent validation
func TestLineChangeEvent_Validation(t *testing.T) {
	validLine := Line{
		ID:       "test",
		IP:       "10.1.1.1",
		Interval: time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}
	validLine.ComputeHash()

	tests := []struct {
		name    string
		event   LineChangeEvent
		isValid bool
	}{
		{
			name: "valid_create_event",
			event: LineChangeEvent{
				Type:    LineCreate,
				Line:    validLine,
				Version: 1,
			},
			isValid: true,
		},
		{
			name: "valid_update_event",
			event: LineChangeEvent{
				Type:    LineUpdate,
				Line:    validLine,
				Version: 2,
			},
			isValid: true,
		},
		{
			name: "valid_delete_event",
			event: LineChangeEvent{
				Type:    LineDelete,
				Line:    validLine,
				Version: 3,
			},
			isValid: true,
		},
		{
			name: "zero_version",
			event: LineChangeEvent{
				Type:    LineCreate,
				Line:    validLine,
				Version: 0,
			},
			isValid: false,
		},
		{
			name: "negative_version",
			event: LineChangeEvent{
				Type:    LineCreate,
				Line:    validLine,
				Version: -1,
			},
			isValid: false,
		},
		{
			name: "empty_line_ip",
			event: LineChangeEvent{
				Type: LineCreate,
				Line: Line{
					ID:       "test",
					IP:       "", // Empty IP
					Interval: time.Minute,
					Router:   validLine.Router,
				},
				Version: 1,
			},
			isValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertions := TestAssertions{}
			isValid := assertions.AssertEventValid(tt.event)
			assert.Equal(t, tt.isValid, isValid, "Event validation should match expected result")
		})
	}
}

// TestParseDurationFromMacroEdgeCases tests edge cases for duration parsing
func TestParseDurationFromMacroEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		defaultVal  time.Duration
		expectedDur time.Duration
	}{
		{
			name:        "max_int64",
			value:       "9223372036854775807",
			defaultVal:  time.Minute,
			expectedDur: time.Minute, // Should overflow and use default
		},
		{
			name:        "scientific_notation",
			value:       "1e3",
			defaultVal:  time.Minute,
			expectedDur: time.Minute, // Should fail to parse and use default
		},
		{
			name:        "hexadecimal",
			value:       "0x100",
			defaultVal:  time.Minute,
			expectedDur: time.Minute, // Should fail to parse and use default
		},
		{
			name:        "octal",
			value:       "0777",
			defaultVal:  time.Minute,
			expectedDur: 777 * time.Second, // Should parse as decimal
		},
		{
			name:        "leading_zeros",
			value:       "000300",
			defaultVal:  time.Minute,
			expectedDur: 300 * time.Second,
		},
		{
			name:        "whitespace",
			value:       "  300  ",
			defaultVal:  time.Minute,
			expectedDur: time.Minute, // Should fail due to whitespace
		},
		{
			name:        "plus_sign",
			value:       "+300",
			defaultVal:  time.Minute,
			expectedDur: time.Minute, // ParseInt doesn't handle + prefix in this context
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDurationFromMacro(tt.value, tt.defaultVal)
			assert.Equal(t, tt.expectedDur, result)
		})
	}
}

// TestDefaultConstants tests default constant values
func TestDefaultConstants(t *testing.T) {
	assert.Equal(t, 3*time.Minute, DefaultInterval)
	assert.Equal(t, "1.1.1.1", ProxyIP)
	assert.Equal(t, "TempType", LineSelectTag)
	assert.Equal(t, "ddl", LineSelectValue)
}

// TestSubscription_EdgeCases tests edge cases for Subscription
func TestSubscription_EdgeCases(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	t.Run("subscription_with_cancelled_context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		sub := syncer.Subscribe(ctx)
		defer sub.Close()

		// Should handle cancelled context gracefully
		assert.NotNil(t, sub)
		assert.NotNil(t, sub.Events())
	})

	t.Run("multiple_close_calls", func(t *testing.T) {
		ctx := context.Background()
		sub := syncer.Subscribe(ctx)

		// Multiple closes should be safe
		assert.NotPanics(t, func() {
			sub.Close()
			sub.Close()
			sub.Close()
		})
	})
}

// TestRouter_EdgeCases tests edge cases for Router struct
func TestRouter_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		router Router
		desc   string
	}{
		{
			name: "empty_router",
			router: Router{
				IP:       "",
				Username: "",
				Password: "",
				Platform: "",
				Protocol: "",
			},
			desc: "Router with all empty fields",
		},
		{
			name: "ipv6_router",
			router: Router{
				IP:       "2001:db8::1",
				Username: "admin",
				Password: "password",
				Platform: connection.Platform("cisco_iosxe"),
				Protocol: connection.Protocol("netconf"),
			},
			desc: "Router with IPv6 address",
		},
		{
			name: "router_with_port",
			router: Router{
				IP:       "192.168.1.1:8080",
				Username: "admin",
				Password: "password",
				Platform: connection.Platform("cisco_iosxe"),
				Protocol: connection.Protocol("https"),
			},
			desc: "Router with port in IP",
		},
		{
			name: "router_with_domain",
			router: Router{
				IP:       "router.example.com",
				Username: "admin",
				Password: "password",
				Platform: connection.Platform("cisco_iosxe"),
				Protocol: connection.Protocol("ssh"),
			},
			desc: "Router with domain name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				config := tt.router.ToConnectionConfig()
				assert.Equal(t, tt.router.IP, config.IP)
				assert.Equal(t, tt.router.Username, config.Username)
				assert.Equal(t, tt.router.Password, config.Password)
				assert.NotNil(t, config.Metadata)

				platform, hasPlatform := config.Metadata["platform"]
				protocol, hasProtocol := config.Metadata["protocol"]

				assert.True(t, hasPlatform)
				assert.True(t, hasProtocol)
				assert.Equal(t, tt.router.Platform, platform)
				assert.Equal(t, tt.router.Protocol, protocol)
			}, tt.desc)
		})
	}
}

// TestLineHashCollisions tests for potential hash collisions
func TestLineHashCollisions(t *testing.T) {
	const numLines = 10000
	hashes := make(map[uint64]bool, numLines)
	collisions := 0

	for i := 0; i < numLines; i++ {
		line := Line{
			ID:       fmt.Sprintf("line%d", i),
			IP:       fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256),
			Interval: time.Duration(i%3600) * time.Second,
			Router: Router{
				IP:       fmt.Sprintf("192.168.%d.%d", i/256, i%256),
				Username: fmt.Sprintf("user%d", i%100),
				Password: fmt.Sprintf("pass%d", i%100),
				Platform: connection.Platform([]string{"cisco_iosxe", "huawei_vrp", "juniper_junos"}[i%3]),
				Protocol: connection.Protocol([]string{"ssh", "netconf", "restconf"}[i%3]),
			},
		}

		line.ComputeHash()

		if hashes[line.Hash] {
			collisions++
		}
		hashes[line.Hash] = true
	}

	collisionRate := float64(collisions) / float64(numLines) * 100
	t.Logf("Hash collision rate: %.4f%% (%d collisions out of %d hashes)", collisionRate, collisions, numLines)

	// With a good hash function, collision rate should be very low
	assert.Less(t, collisionRate, 0.1, "Hash collision rate should be less than 0.1%")
}

// BenchmarkLineOperations benchmarks various Line operations
func BenchmarkLineOperations(b *testing.B) {
	line := Line{
		ID:       "benchmark-line",
		IP:       "10.1.1.1",
		Interval: 3 * time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}

	b.Run("ComputeHash", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			line.ComputeHash()
		}
	})

	b.Run("RouterToConnectionConfig", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = line.Router.ToConnectionConfig()
		}
	})
}

// BenchmarkEventCreation benchmarks event creation
func BenchmarkEventCreation(b *testing.B) {
	line := Line{
		ID:       "benchmark-line",
		IP:       "10.1.1.1",
		Interval: 3 * time.Minute,
		Router: Router{
			IP:       "192.168.1.1",
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}
	line.ComputeHash()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = LineChangeEvent{
			Type:    LineCreate,
			Line:    line,
			Version: int64(i),
		}
	}
}
