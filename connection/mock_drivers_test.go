package connection

import (
	"context"
	"errors"
)

type MockProtocolDriver struct {
	ProtocolTypeFunc  func() Protocol
	CloseFunc         func() error
	ExecuteFunc       func(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error)
	GetCapabilityFunc func() ProtocolCapability
}

func (m *MockProtocolDriver) ProtocolType() Protocol {
	if m.ProtocolTypeFunc != nil {
		return m.ProtocolTypeFunc()
	}
	return ""
}

func (m *MockProtocolDriver) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

func (m *MockProtocolDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, req)
	}
	return nil, errors.New("mock not implemented")
}

func (m *MockProtocolDriver) GetCapability() ProtocolCapability {
	if m.GetCapabilityFunc != nil {
		return m.GetCapabilityFunc()
	}
	return ProtocolCapability{}
}

type MockProtocolFactory struct {
	CreateFunc            func(config EnhancedConnectionConfig) (ProtocolDriver, error)
	CreateWithContextFunc func(ctx context.Context, config EnhancedConnectionConfig) (ProtocolDriver, error)
	HealthCheckFunc       func(driver ProtocolDriver) bool
}

func (m *MockProtocolFactory) Create(config EnhancedConnectionConfig) (ProtocolDriver, error) {
	return m.CreateWithContext(context.Background(), config)
}

func (m *MockProtocolFactory) CreateWithContext(ctx context.Context, config EnhancedConnectionConfig) (ProtocolDriver, error) {
	if m.CreateWithContextFunc != nil {
		return m.CreateWithContextFunc(ctx, config)
	}
	// 向后兼容：如果没有CreateWithContextFunc，使用CreateFunc
	if m.CreateFunc != nil {
		return m.CreateFunc(config)
	}
	return nil, errors.New("mock not implemented")
}

func (m *MockProtocolFactory) HealthCheck(driver ProtocolDriver, config EnhancedConnectionConfig) bool {
	if m.HealthCheckFunc != nil {
		return m.HealthCheckFunc(driver)
	}
	return false
}
