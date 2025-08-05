package manager

import (
	"testing"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

func TestLineDeleteWithDelay(t *testing.T) {
	mockSyncer := NewMockSyncer()
	mgr := NewManager(mockSyncer)

	// 添加测试专线
	line := syncer.Line{ID: "1", Router: syncer.Router{IP: "1.1.1.1"}}
	mgr.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line,
	})

	// 删除专线
	mgr.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineDelete,
		Line: line,
	})

	// 验证延迟删除未立即生效
	if _, exists := mgr.schedulers["1.1.1.1"]; !exists {
		t.Error("scheduler deleted too early")
	}
}
