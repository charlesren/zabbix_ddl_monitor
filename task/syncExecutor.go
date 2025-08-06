package task

import (
	"context"
	"fmt"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

var GlobalRegistry = NewRegistry()

type SyncExecutor struct {
	// 无连接池等状态字段
	taskTimeout time.Duration // 单任务超时时间
}

func NewSyncExecutor() *SyncExecutor {
	return &SyncExecutor{
		taskTimeout: 30 * time.Second,
	}
}

// Execute 同步执行单个任务（需外部管理连接）
func (e *SyncExecutor) Execute(
	ctx context.Context,
	conn connection.ProtocolDriver,
	platform string,
	taskType string,
	params map[string]interface{},
) (Result, error) {
	// 1. 获取任务实现
	t, err := GlobalRegistry.Get(taskType)
	if err != nil {
		return Result{}, fmt.Errorf("unknown task type: %s", taskType)
	}

	// 2. 检查是否支持批量（即使单任务也走批量接口）
	/*
		if batchTask, ok := t.(BatchTask); ok {
			results := batchTask.ExecuteBatch(conn, platform, []map[string]interface{}{params})
			if len(results) > 0 {
				return results[0], nil
			}
			return Result{}, fmt.Errorf("empty batch results")
		}
	*/

	// 3. 普通任务执行
	start := time.Now()
	commands, err := t.GenerateCommands(platform, params)
	if err != nil {
		return Result{}, fmt.Errorf("generate commands failed: %w", err)
	}

	output, err := conn.SendCommands(commands)
	if err != nil {
		return Result{}, fmt.Errorf("send commands failed: %w", err)
	}
	result, err := t.ParseOutput(platform, output)
	if err != nil {
		return Result{}, fmt.Errorf("parse output failed: %w", err)
	}
	ylog.Debugf("executor", "sync task completed in %v (type=%s)",
		time.Since(start), taskType)
	return result, nil
}
