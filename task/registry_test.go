package task

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

func TestNewDefaultRegistry(t *testing.T) {
	registry := NewDefaultRegistry()

	if registry == nil {
		t.Fatal("Expected non-nil registry")
	}

	// Verify it implements the Registry interface
	var _ Registry = registry

	// Check internal state
	defaultRegistry := registry.(*DefaultRegistry)
	if defaultRegistry.tasks == nil {
		t.Error("Expected tasks map to be initialized")
	}

	if len(defaultRegistry.tasks) != 0 {
		t.Error("Expected empty tasks map initially")
	}
}

func TestDefaultRegistry_Register(t *testing.T) {
	registry := NewDefaultRegistry()

	// Create test task metadata
	testMeta := TaskMeta{
		Type:        "test_task",
		Description: "Test task for registry",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params:      []ParamSpec{},
							},
						},
					},
				},
			},
		},
	}

	// Test successful registration
	err := registry.Register(testMeta)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Test duplicate registration
	err = registry.Register(testMeta)
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}

	expectedErrMsg := "task 'test_task' already registered"
	if err.Error() != expectedErrMsg {
		t.Errorf("Expected error message '%s', got '%s'", expectedErrMsg, err.Error())
	}
}

func TestDefaultRegistry_Discover(t *testing.T) {
	registry := NewDefaultRegistry()

	// Register a test task
	testMeta := TaskMeta{
		Type:        "ping",
		Description: "Ping task",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params:      []ParamSpec{},
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformHuaweiVRP,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params:      []ParamSpec{},
							},
						},
					},
				},
			},
		},
	}

	err := registry.Register(testMeta)
	if err != nil {
		t.Fatalf("Failed to register test task: %v", err)
	}

	tests := []struct {
		name        string
		taskType    TaskType
		platform    Platform
		protocol    Protocol
		commandType CommandType
		wantErr     bool
		errContains string
	}{
		{
			name:        "successful_discovery_cisco_interactive",
			taskType:    "ping",
			platform:    connection.PlatformCiscoIOSXE,
			protocol:    connection.ProtocolScrapli,
			commandType: connection.CommandTypeInteractiveEvent,
			wantErr:     false,
		},
		{
			name:        "successful_discovery_huawei_commands",
			taskType:    "ping",
			platform:    connection.PlatformHuaweiVRP,
			protocol:    connection.ProtocolScrapli,
			commandType: connection.CommandTypeCommands,
			wantErr:     false,
		},
		{
			name:        "task_not_found",
			taskType:    "nonexistent",
			platform:    connection.PlatformCiscoIOSXE,
			protocol:    connection.ProtocolScrapli,
			commandType: connection.CommandTypeInteractiveEvent,
			wantErr:     true,
			errContains: "task type 'nonexistent' not found",
		},
		{
			name:        "platform_not_supported",
			taskType:    "ping",
			platform:    connection.PlatformCiscoNXOS, // Not registered
			protocol:    connection.ProtocolScrapli,
			commandType: connection.CommandTypeInteractiveEvent,
			wantErr:     true,
			errContains: "no matching task",
		},
		{
			name:        "protocol_not_supported",
			taskType:    "ping",
			platform:    connection.PlatformCiscoIOSXE,
			protocol:    connection.ProtocolSSH, // Not registered for this platform
			commandType: connection.CommandTypeInteractiveEvent,
			wantErr:     true,
			errContains: "no matching task",
		},
		{
			name:        "command_type_not_supported",
			taskType:    "ping",
			platform:    connection.PlatformCiscoIOSXE,
			protocol:    connection.ProtocolScrapli,
			commandType: connection.CommandTypeCommands, // Not registered for this platform
			wantErr:     true,
			errContains: "no matching task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, err := registry.Discover(tt.taskType, tt.platform, tt.protocol, tt.commandType)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
					return
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errContains, err.Error())
				}
				if task != nil {
					t.Error("Expected nil task when error occurs")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
					return
				}
				if task == nil {
					t.Error("Expected non-nil task")
					return
				}

				// Verify the returned task is of correct type
				if _, ok := task.(*PingTask); !ok {
					t.Errorf("Expected *PingTask, got %T", task)
				}
			}
		})
	}
}

func TestDefaultRegistry_ListPlatforms(t *testing.T) {
	registry := NewDefaultRegistry()

	// Initially empty
	platforms := registry.ListPlatforms(connection.PlatformCiscoIOSXE)
	if len(platforms) != 0 {
		t.Errorf("Expected empty list initially, got %d items", len(platforms))
	}

	// Register tasks for different platforms
	task1Meta := TaskMeta{
		Type:        "ping",
		Description: "Ping task",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformHuaweiVRP,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
							},
						},
					},
				},
			},
		},
	}

	task2Meta := TaskMeta{
		Type:        "traceroute",
		Description: "Traceroute task",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} }, // Reuse for testing
							},
						},
					},
				},
			},
		},
	}

	task3Meta := TaskMeta{
		Type:        "show_version",
		Description: "Show version task",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoNXOS, // Different platform
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolSSH,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
							},
						},
					},
				},
			},
		},
	}

	// Register all tasks
	if err := registry.Register(task1Meta); err != nil {
		t.Fatalf("Failed to register task1: %v", err)
	}
	if err := registry.Register(task2Meta); err != nil {
		t.Fatalf("Failed to register task2: %v", err)
	}
	if err := registry.Register(task3Meta); err != nil {
		t.Fatalf("Failed to register task3: %v", err)
	}

	tests := []struct {
		name            string
		platform        Platform
		expectedTasks   []TaskType
		unexpectedTasks []TaskType
	}{
		{
			name:            "cisco_iosxe_tasks",
			platform:        connection.PlatformCiscoIOSXE,
			expectedTasks:   []TaskType{"ping", "traceroute"},
			unexpectedTasks: []TaskType{"show_version"},
		},
		{
			name:            "huawei_vrp_tasks",
			platform:        connection.PlatformHuaweiVRP,
			expectedTasks:   []TaskType{"ping"},
			unexpectedTasks: []TaskType{"traceroute", "show_version"},
		},
		{
			name:            "cisco_nxos_tasks",
			platform:        connection.PlatformCiscoNXOS,
			expectedTasks:   []TaskType{"show_version"},
			unexpectedTasks: []TaskType{"ping", "traceroute"},
		},
		{
			name:            "h3c_comware_platform",
			platform:        connection.PlatformH3CComware,
			expectedTasks:   []TaskType{"ping"},
			unexpectedTasks: []TaskType{"traceroute", "show_version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platforms := registry.ListPlatforms(tt.platform)

			// Check expected tasks are present
			for _, expectedTask := range tt.expectedTasks {
				found := false
				for _, task := range platforms {
					if task == expectedTask {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected task '%s' not found in platforms list", expectedTask)
				}
			}

			// Check unexpected tasks are not present
			for _, unexpectedTask := range tt.unexpectedTasks {
				for _, task := range platforms {
					if task == unexpectedTask {
						t.Errorf("Unexpected task '%s' found in platforms list", unexpectedTask)
					}
				}
			}

			// Check length matches expected
			if len(platforms) != len(tt.expectedTasks) {
				t.Errorf("Expected %d tasks for platform %s, got %d",
					len(tt.expectedTasks), tt.platform, len(platforms))
			}
		})
	}
}

func TestDefaultRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewDefaultRegistry()

	// Test concurrent registration and discovery
	const numGoroutines = 10
	const numTasks = 5

	// Create channel for synchronization
	done := make(chan bool, numGoroutines*2)

	// Concurrent registrations
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numTasks; j++ {
				taskName := TaskType(fmt.Sprintf("task_%d_%d", id, j))
				meta := TaskMeta{
					Type:        taskName,
					Description: "Concurrent test task",
					Platforms: []PlatformSupport{
						{
							Platform: connection.PlatformCiscoIOSXE,
							Protocols: []ProtocolSupport{
								{
									Protocol: connection.ProtocolScrapli,
									CommandTypes: []CommandTypeSupport{
										{
											CommandType: connection.CommandTypeCommands,
											ImplFactory: func() Task { return &PingTask{} },
										},
									},
								},
							},
						},
					},
				}
				registry.Register(meta) // Ignore errors for duplicate registrations
			}
		}(i)
	}

	// Concurrent discoveries (after some registrations)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numTasks; j++ {
				taskName := TaskType(fmt.Sprintf("task_%d_%d", id, j))
				_, _ = registry.Discover(
					taskName,
					connection.PlatformCiscoIOSXE,
					connection.ProtocolScrapli,
					connection.CommandTypeCommands,
				)
				// Ignore errors as tasks may not be registered yet
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// No assertion needed, just testing that no race conditions occur
	// If there are race conditions, the race detector will catch them
}

func TestDefaultRegistry_EmptyPlatformSupport(t *testing.T) {
	registry := NewDefaultRegistry()

	// Register task with empty platform support
	emptyMeta := TaskMeta{
		Type:        "empty_task",
		Description: "Task with no platform support",
		Platforms:   []PlatformSupport{}, // Empty
	}

	err := registry.Register(emptyMeta)
	if err != nil {
		t.Errorf("Expected no error for empty platform support, got: %v", err)
	}

	// Try to discover the task
	task, err := registry.Discover(
		"empty_task",
		connection.PlatformCiscoIOSXE,
		connection.ProtocolScrapli,
		connection.CommandTypeCommands,
	)

	if err == nil {
		t.Error("Expected error when discovering task with no platform support")
	}
	if task != nil {
		t.Error("Expected nil task when no platform support")
	}

	// List platforms should return empty
	platforms := registry.ListPlatforms(connection.PlatformCiscoIOSXE)
	for _, platform := range platforms {
		if platform == "empty_task" {
			t.Error("Empty task should not appear in platform list")
		}
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					strings.Contains(s, substr))))
}
