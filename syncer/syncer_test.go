package syncer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zapix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func NewTestSyncer() *ConfigSyncer {
	return &ConfigSyncer{
		subscribers: make([]chan<- LineChangeEvent, 0),
		mu:          sync.RWMutex{},
	}
}

type MockZabbixClient struct {
	mock.Mock
}

func (m *MockZabbixClient) GetProxyFormHost(ip string) ([]zapix.ProxyObject, error) {
	args := m.Called(ip)
	return args.Get(0).([]zapix.ProxyObject), args.Error(1)
}

func (m *MockZabbixClient) HostGet(params zapix.HostGetParams) ([]zapix.Host, error) {
	args := m.Called(params)
	return args.Get(0).([]zapix.Host), args.Error(1)
}

// 1. 事件分发完整性测试
func TestEventBroadcast(t *testing.T) {
	syncer := NewTestSyncer()
	sub1 := syncer.Subscribe(context.Background())
	defer sub1.Close()
	sub2 := syncer.Subscribe(context.Background())
	defer sub2.Close()

	event := LineChangeEvent{Type: LineUpdate}
	syncer.mu.Lock()
	for _, sub := range syncer.subscribers {
		sub <- event
	}
	syncer.mu.Unlock()

	assert.Equal(t, event, <-sub1.Events())
	assert.Equal(t, event, <-sub2.Events())
}

// 2. 重复关闭安全性测试
func TestDoubleClose(t *testing.T) {
	syncer := NewTestSyncer()
	sub := syncer.Subscribe(context.Background())

	sub.Close()
	sub.Close() // 第二次调用不应panic

	_, ok := <-sub.Events()
	assert.False(t, ok)
}

// 3. 上下文超时测试
func TestContextTimeout(t *testing.T) {
	syncer := NewTestSyncer()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	sub := syncer.Subscribe(ctx)
	<-ctx.Done()

	_, ok := <-sub.Events()
	assert.False(t, ok)
}

// 4. 空事件分发测试
func TestEmptyEvent(t *testing.T) {
	syncer := NewTestSyncer()
	sub := syncer.Subscribe(context.Background())
	defer sub.Close()

	syncer.mu.Lock()
	syncer.subscribers[0] <- LineChangeEvent{}
	syncer.mu.Unlock()

	event := <-sub.Events()
	assert.Equal(t, LineChangeEvent{}, event)
}

// 5. 高负载事件测试
func TestHighLoadEvents(t *testing.T) {
	syncer := NewTestSyncer()
	sub := syncer.Subscribe(context.Background())
	defer sub.Close()

	go func() {
		syncer.mu.Lock()
		defer syncer.mu.Unlock()
		for i := 0; i < 1e4; i++ {
			syncer.subscribers[0] <- LineChangeEvent{Type: LineUpdate}
		}
	}()

	for i := 0; i < 1e4; i++ {
		<-sub.Events()
	}
}

// 6. Mock ZabbixClient 测试
func TestFetchLinesWithMock(t *testing.T) {
	mockClient := new(MockZabbixClient)
	syncer := &ConfigSyncer{client: mockClient}

	mockClient.On("GetProxyFormHost", mock.Anything).Return([]zapix.Proxy{{Proxyid: "123"}}, nil)
	mockClient.On("HostGet", mock.Anything).Return([]zapix.Host{{Host: "1.1.1.1"}}, nil)

	lines, err := syncer.fetchLines()
	assert.NoError(t, err)
	assert.Equal(t, "1.1.1.1", lines[""].IP)
	mockClient.AssertExpectations(t)
}
