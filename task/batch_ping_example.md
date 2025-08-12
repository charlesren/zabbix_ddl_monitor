# 批量Ping任务示例

本文档展示了专线监控系统中如何将多个专线IP合并为批量ping任务，以及结果分发的完整工作流程。

## 场景描述

假设有一个路由器 `192.168.1.1` 需要监控3条专线：
- 专线A: IP `10.1.1.1`，间隔 3分钟
- 专线B: IP `10.1.1.2`，间隔 3分钟  
- 专线C: IP `10.1.1.3`，间隔 3分钟

传统方式需要3次连接执行3个ping任务，现在合并为1次连接执行1个批量任务。

## 工作流程

### 1. 任务合并阶段

```go
// RouterScheduler.executeTasksAsync() 中的处理
func (s *RouterScheduler) executeTasksAsync(q *IntervalTaskQueue) {
    // 获取同一间隔的所有专线
    lines := q.GetTasksSnapshot()
    // lines = [
    //     {ID: "line-001", IP: "10.1.1.1", Router: {IP: "192.168.1.1"}},
    //     {ID: "line-002", IP: "10.1.1.2", Router: {IP: "192.168.1.1"}},
    //     {ID: "line-003", IP: "10.1.1.3", Router: {IP: "192.168.1.1"}},
    // ]

    // 提取所有IP地址
    targetIPs := []string{"10.1.1.1", "10.1.1.2", "10.1.1.3"}

    // 创建批量任务上下文
    taskCtx := task.TaskContext{
        TaskType:    "ping",
        Platform:    "cisco_iosxe",
        Protocol:    "scrapli",
        CommandType: "interactive_event",
        Params: map[string]interface{}{
            "target_ips": targetIPs,  // 批量IP列表
            "repeat":     5,
            "timeout":    10 * time.Second,
        },
    }
}
```

### 2. 命令生成阶段

```go
// PingTask.BuildCommand() 中的处理
func (PingTask) BuildCommand(ctx TaskContext) (Command, error) {
    // 获取批量IP: ["10.1.1.1", "10.1.1.2", "10.1.1.3"]
    targetIPs := ctx.Params["target_ips"].([]string)
    
    // 为Cisco设备生成批量ping命令
    events := []*channel.SendInteractiveEvent{
        // 第一个IP
        {
            ChannelInput:    "ping 10.1.1.1 repeat 5 timeout 10",
            ChannelResponse: ">",
        },
        // 第二个IP
        {
            ChannelInput:    "ping 10.1.1.2 repeat 5 timeout 10", 
            ChannelResponse: ">",
        },
        // 第三个IP
        {
            ChannelInput:    "ping 10.1.1.3 repeat 5 timeout 10",
            ChannelResponse: ">",
        },
    }
}
```

### 3. 执行阶段

```bash
# 设备上实际执行的命令序列
Router> ping 10.1.1.1 repeat 5 timeout 10
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.1.1.1, timeout is 10 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms

Router> ping 10.1.1.2 repeat 5 timeout 10
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.1.1.2, timeout is 10 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 2/3/5 ms

Router> ping 10.1.1.3 repeat 5 timeout 10
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.1.1.3, timeout is 10 seconds:
.....
Success rate is 0 percent (0/5)
```

### 4. 结果解析阶段

```go
// PingTask.ParseOutput() 中的批量解析
func (PingTask) ParseOutput(ctx TaskContext, raw interface{}) (Result, error) {
    output := string(raw.([]byte))
    targetIPs := ctx.Params["target_ips"].([]string)
    
    // 解析每个IP的结果
    batchResults := map[string]Result{
        "10.1.1.1": {
            Success: true,
            Data: map[string]interface{}{
                "target_ip": "10.1.1.1",
                "success_rate": "100%",
                "rtt_info": "round-trip min/avg/max = 1/2/4 ms",
                "status": "success",
            },
        },
        "10.1.1.2": {
            Success: true,
            Data: map[string]interface{}{
                "target_ip": "10.1.1.2", 
                "success_rate": "100%",
                "rtt_info": "round-trip min/avg/max = 2/3/5 ms",
                "status": "success",
            },
        },
        "10.1.1.3": {
            Success: false,
            Data: map[string]interface{}{
                "target_ip": "10.1.1.3",
                "success_rate": "0%", 
                "status": "failed",
            },
        },
    }
    
    return Result{
        Success: true, // 至少有一个成功
        Data: map[string]interface{}{
            "batch_results": batchResults,
            "success_count": 2,
            "total_count": 3,
            "success_rate": "66.7%",
            "batch_mode": true,
        },
    }
}
```

### 5. 结果分发阶段

```go
// RouterScheduler.distributeBatchResult() 中的处理
func (s *RouterScheduler) distributeBatchResult(lines []syncer.Line, batchResult task.Result, duration time.Duration) {
    batchResults := batchResult.Data["batch_results"].(map[string]task.Result)
    
    // 为每个专线分发对应的结果
    for _, line := range lines {
        if ipResult, exists := batchResults[line.IP]; exists {
            // 发送到聚合器
            s.aggregator.SubmitTaskResult(line, "ping", ipResult, duration)
        }
    }
}
```

### 6. 最终结果

每个专线获得独立的结果：

**专线A (10.1.1.1)**:
```json
{
  "line_id": "line-001",
  "ip": "10.1.1.1",
  "router_ip": "192.168.1.1",
  "task_type": "ping",
  "success": true,
  "data": {
    "target_ip": "10.1.1.1",
    "success_rate": "100%",
    "rtt_info": "round-trip min/avg/max = 1/2/4 ms",
    "status": "success"
  }
}
```

**专线B (10.1.1.2)**:
```json
{
  "line_id": "line-002", 
  "ip": "10.1.1.2",
  "router_ip": "192.168.1.1",
  "task_type": "ping",
  "success": true,
  "data": {
    "target_ip": "10.1.1.2",
    "success_rate": "100%", 
    "rtt_info": "round-trip min/avg/max = 2/3/5 ms",
    "status": "success"
  }
}
```

**专线C (10.1.1.3)**:
```json
{
  "line_id": "line-003",
  "ip": "10.1.1.3", 
  "router_ip": "192.168.1.1",
  "task_type": "ping",
  "success": false,
  "data": {
    "target_ip": "10.1.1.3",
    "success_rate": "0%",
    "status": "failed"
  }
}
```

## 性能对比

### 传统方式 (单独执行)
- 连接数: 3次
- 执行时间: ~30秒 (3 × 10秒)
- 资源消耗: 高 (多次连接建立/断开)

### 批量方式 (合并执行)  
- 连接数: 1次
- 执行时间: ~12秒 (1次连接 + 连续ping)
- 资源消耗: 低 (单次连接复用)
- 性能提升: **60%+**

## 配置示例

```yaml
# 路由器配置
router:
  ip: "192.168.1.1"
  username: "admin"
  password: "password"
  platform: "cisco_iosxe"
  protocol: "scrapli"

# 专线配置
lines:
  - id: "line-001"
    ip: "10.1.1.1"
    interval: "3m"
  - id: "line-002"  
    ip: "10.1.1.2"
    interval: "3m"
  - id: "line-003"
    ip: "10.1.1.3"
    interval: "3m"

# 任务配置
ping_task:
  repeat: 5
  timeout: "10s"
  batch_enabled: true
  max_batch_size: 10
```

## 关键优势

1. **性能提升**: 减少连接开销，提高执行效率
2. **资源节约**: 降低设备负载和网络流量
3. **一致性**: 同一时间点执行，结果更具可比性
4. **可扩展**: 支持动态批次大小调整
5. **容错性**: 单个IP失败不影响其他IP的结果

## 注意事项

1. **批次大小**: 建议不超过10个IP，避免命令执行超时
2. **平台差异**: 不同设备平台的批量命令格式可能不同
3. **错误处理**: 需要正确解析和分发每个IP的独立结果
4. **超时控制**: 批量执行的总超时时间需要合理设置
5. **日志记录**: 确保批量操作的可追溯性和调试能力