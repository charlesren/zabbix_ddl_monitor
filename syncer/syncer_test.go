package syncer

import (
	"testing"
	"time"

	"github.com/charlesren/zapix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockZabbixClient 模拟 zapix.Client
type MockZabbixClient struct {
	mock.Mock
}

func (m *MockZabbixClient) DoRequest(method string, params interface{}) (*zapix.Response, error) {
	args := m.Called(method, params)
	return args.Get(0).(*zapix.Response), args.Error(1)
}

func TestConfigSyncer_Sync(t *testing.T) {
	// 1. 初始化模拟客户端
	mockClient := new(MockZabbixClient)

	// 2. 模拟Zabbix API返回数据
	mockResponse := &zapix.Response{
		Result: []interface{}{
			map[string]interface{}{
				"lineid":   "line1",
				"ip":       "10.0.0.1",
				"interval": float64(30),
				"hosts": []interface{}{
					map[string]interface{}{
						"hostid":   "router1",
						"ip":       "192.168.1.1",
						"username": "admin",
						"password": "pass123",
						"platform": "cisco_iosxe",
					},
				},
			},
		},
	}
	mockClient.On("DoRequest", "ddl.get", mock.Anything).Return(mockResponse, nil)

	// 3. 创建ConfigSyncer实例
	syncer := &ConfigSyncer{
		client: mockClient,
		lines:  make(map[string]Line),
	}

	// 4. 执行同步
	err := syncer.sync()
	assert.NoError(t, err)

	// 5. 验证结果
	expectedLines := map[string]Line{
		"line1": {
			ID:       "line1",
			IP:       "10.0.0.1",
			Interval: 30 * time.Second,
			Router: Router{
				IP:       "192.168.1.1",
				Username: "admin",
				Password: "pass123",
				Platform: "cisco_iosxe",
			},
		},
	}
	assert.Equal(t, expectedLines, syncer.GetLines())

	// 6. 验证API调用
	mockClient.AssertCalled(t, "DoRequest", "ddl.get", mock.Anything)
}

func TestConfigSyncer_Subscribe(t *testing.T) {
	syncer := &ConfigSyncer{
		lines: make(map[string]Line),
	}

	// 1. 订阅配置变更
	ch, cancel := syncer.Subscribe()
	defer cancel()

	// 2. 模拟配置变更
	syncer.mu.Lock()
	syncer.lines["line1"] = Line{ID: "line1"}
	syncer.notify()
	syncer.mu.Unlock()

	// 3. 验证收到通知
	select {
	case <-ch:
		// 正常收到通知
	case <-time.After(100 * time.Millisecond):
		t.Fatal("未收到配置变更通知")
	}
}

func TestConfigSyncer_IsChanged(t *testing.T) {
	syncer := &ConfigSyncer{
		lines: map[string]Line{
			"line1": {ID: "line1", IP: "10.0.0.1"},
		},
	}

	// 1. 测试无变化
	newLines := map[string]Line{
		"line1": {ID: "line1", IP: "10.0.0.1"},
	}
	assert.False(t, syncer.isChanged(newLines))

	// 2. 测试新增专线
	newLines["line2"] = Line{ID: "line2"}
	assert.True(t, syncer.isChanged(newLines))

	// 3. 测试专线更新
	newLines = map[string]Line{
		"line1": {ID: "line1", IP: "10.0.0.2"}, // IP变更
	}
	assert.True(t, syncer.isChanged(newLines))
}
