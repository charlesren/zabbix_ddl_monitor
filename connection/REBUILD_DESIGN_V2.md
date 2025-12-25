# 连接重建功能设计方案（修正版）

## 设计背景

当前连接池的健康检查功能有完整的触发机制，但重建功能虽然实现了核心逻辑（RebuildManager），却缺少触发机制。本方案旨在严格仿照健康检查的架构，设计一套完整的重建系统。

**基于深入讨论的关键更新**：
1. **修复设计矛盾**：不健康的连接应该触发重建，而不是跳过
2. **明确状态关系**：`markedForRebuild`（决策标记）与`StateRebuilding`（执行状态）的分工
3. **完善健康状态设计**：基于连续失败次数的`Degraded`/`Unhealthy`状态转换
4. **分阶段实施**：先实现后台定时重建，后集成健康检查触发
5. **提供完整API**：RebuildConnectionByID, RebuildConnectionByProto, RebuildConnections

## 设计原则

1. **架构一致性**：严格仿照健康检查的三层架构
2. **池API风格**：重建操作也采用池API的设计思想
3. **协议级别处理**：每个协议单独处理重建
4. **完整API**：提供供外部调用的重建API
5. **分阶段实施**：先后台定时运行，后集成健康检查

## 当前健康检查架构分析（作为模板）

### 三层架构：
```
startBackgroundTasks()
    ↓
healthCheckTask() [定时器，默认30秒]
    ↓
performHealthChecks()
    ↓
performHealthCheckForProtocol(proto) [每个协议]
    ↓
p.GetWithContext(ctx, proto) [池API获取]
    ↓
执行检查 → recordHealthCheck()
    ↓
p.Release(driver) [释放]
```

### 特点：
- 定时触发（默认30秒）
- 协议级别处理
- 使用池API获取连接
- 异步执行
- 检查后释放连接
- 完整的指标收集和事件通知

## 健康状态设计（关键更新）

### 健康状态定义
```go
type HealthStatus int
const (
    HealthStatusUnknown  HealthStatus = iota  // 0: 未知状态（初始状态）
    HealthStatusHealthy                       // 1: 健康状态（完全正常）
    HealthStatusDegraded                      // 2: 降级状态（可用但性能下降）
    HealthStatusUnhealthy                     // 3: 不健康状态（不可用）
)
```

### 基于连续失败次数的状态转换
```
连续失败次数 → 健康状态 → 重建触发
0次失败     → Healthy   → 不触发
1-2次失败   → Degraded  → 可选触发（基于配置）
≥3次失败    → Unhealthy → 自动触发（达到阈值）
```

### 健康检查结果处理
```go
func (conn *EnhancedPooledConnection) recordHealthCheck(success bool, err error) {
    if !success {
        conn.consecutiveFailures++
        
        // 根据连续失败次数设置健康状态
        if conn.consecutiveFailures >= conn.config.UnhealthyThreshold {
            conn.healthStatus = HealthStatusUnhealthy
            // 自动标记重建
            if conn.config.HealthCheckTriggerRebuild {
                conn.markForRebuildWithReason("health_failures")
            }
        } else if conn.consecutiveFailures >= 1 {
            conn.healthStatus = HealthStatusDegraded
            // 可选标记重建
            if conn.config.RebuildOnDegraded {
                conn.markForRebuildWithReason("degraded_performance")
            }
        }
    } else {
        conn.healthStatus = HealthStatusHealthy
        conn.consecutiveFailures = 0
    }
}
```

## 重建系统设计方案

### 三层重建架构（仿照健康检查）：

#### 第一层：重建触发器
```go
// 在 startBackgroundTasks() 中添加
if p.config.SmartRebuildEnabled {
    p.wg.Add(1)
    go func() {
        defer p.wg.Done()
        p.rebuildTask()  // 定时触发重建检查
    }()
}
```

#### 第二层：重建检查器
```go
func (p *EnhancedConnectionPool) performRebuilds() {
    if p.rebuildManager == nil || !p.config.SmartRebuildEnabled {
        return
    }

    protocols := p.getAllProtocols()
    if len(protocols) == 0 {
        return
    }

    // 为每个协议执行重建检查（并发控制）
    semaphore := make(chan struct{}, p.config.RebuildConcurrency)
    for _, proto := range protocols {
        go func(proto Protocol) {
            semaphore <- struct{}{}
            defer func() { <-semaphore }()
            p.performRebuildForProtocol(proto)
        }(proto)
    }
}
```

#### 第三层：协议级重建器
```go
func (p *EnhancedConnectionPool) performRebuildForProtocol(proto Protocol) {
    // 1. 获取该协议所有需要重建的连接
    conns := p.getConnectionsForRebuild(proto)
    if len(conns) == 0 {
        return
    }

    // 2. 批量处理（控制并发）
    batchSize := p.config.RebuildBatchSize
    if batchSize <= 0 {
        batchSize = 5 // 默认批量大小
    }

    for i := 0; i < len(conns); i += batchSize {
        end := i + batchSize
        if end > len(conns) {
            end = len(conns)
        }
        
        batch := conns[i:end]
        for _, conn := range batch {
            go p.rebuildConnection(proto, conn)
        }
        
        // 批次间延迟，避免资源冲击
        time.Sleep(100 * time.Millisecond)
    }
}
```

## 配置设计（EnhancedConnectionConfig扩展）

```go
// 在 EnhancedConnectionConfig 中添加以下配置
type EnhancedConnectionConfig struct {
    // ... 现有配置
    
    // 健康检查配置（现有）
    HealthCheckTime    time.Duration `json:"health_check_time" yaml:"health_check_time"`     // 检查间隔，默认30秒
    HealthCheckTimeout time.Duration `json:"health_check_timeout" yaml:"health_check_timeout"` // 检查超时，默认5秒
    
    // 健康状态阈值配置（新增）
    UnhealthyThreshold int           `json:"unhealthy_threshold" yaml:"unhealthy_threshold"`   // 不健康阈值（连续失败次数），默认3
    DegradedThreshold  time.Duration `json:"degraded_threshold" yaml:"degraded_threshold"`     // 降级响应时间阈值，默认2秒
    
    // 健康检查触发重建配置（新增）
    HealthCheckTriggerRebuild bool `json:"health_check_trigger_rebuild" yaml:"health_check_trigger_rebuild"` // 健康检查失败是否触发重建，默认true
    RebuildOnDegraded         bool `json:"rebuild_on_degraded" yaml:"rebuild_on_degraded"`                   // 降级时是否重建，默认false
    
    // 智能重建配置（现有）
    SmartRebuildEnabled            bool          `json:"smart_rebuild_enabled" yaml:"smart_rebuild_enabled"`
    RebuildMaxUsageCount           int64         `json:"rebuild_max_usage_count" yaml:"rebuild_max_usage_count"`
    RebuildMaxAge                  time.Duration `json:"rebuild_max_age" yaml:"rebuild_max_age"`
    RebuildMaxErrorRate            float64       `json:"rebuild_max_error_rate" yaml:"rebuild_max_error_rate"`
    RebuildMinInterval             time.Duration `json:"rebuild_min_interval" yaml:"rebuild_min_interval"`
    RebuildMinRequestsForErrorRate int64         `json:"rebuild_min_requests_for_error_rate" yaml:"rebuild_min_requests_for_error_rate"`
    RebuildStrategy                string        `json:"rebuild_strategy" yaml:"rebuild_strategy"` // "any" | "all" | "usage" | "age" | "error"
    
    // 重建执行配置（新增）
    RebuildCheckInterval   time.Duration `json:"rebuild_check_interval" yaml:"rebuild_check_interval"`   // 重建检查间隔，默认5分钟
    RebuildBatchSize       int           `json:"rebuild_batch_size" yaml:"rebuild_batch_size"`           // 批量重建大小，默认5
    RebuildConcurrency     int           `json:"rebuild_concurrency" yaml:"rebuild_concurrency"`         // 并发重建数，默认3
}
```

### 默认值配置：
```go
// 在 config_builder.go 或相应位置设置默认值
defaultConfig := &EnhancedConnectionConfig{
    // ... 其他默认值
    
    // 健康检查配置默认值
    HealthCheckTime:    30 * time.Second,
    HealthCheckTimeout: 5 * time.Second,
    
    // 健康状态阈值默认值
    UnhealthyThreshold:        3,                     // 连续3次失败标记为不健康
    DegradedThreshold:         2 * time.Second,       // 响应时间超过2秒标记为降级
    
    // 健康检查触发重建默认值
    HealthCheckTriggerRebuild: true,                  // 不健康时自动触发重建
    RebuildOnDegraded:         false,                 // 降级时不自动重建（可配置开启）
    
    // 智能重建配置默认值
    SmartRebuildEnabled:            true,
    RebuildMaxUsageCount:           200,
    RebuildMaxAge:                  30 * time.Minute,
    RebuildMaxErrorRate:            0.2,
    RebuildMinInterval:             5 * time.Minute,
    RebuildMinRequestsForErrorRate: 10,
    RebuildStrategy:                "any",
    
    // 重建执行配置默认值
    RebuildCheckInterval:   5 * time.Minute,
    RebuildBatchSize:       5,
    RebuildConcurrency:     3,
}
```

## API设计

### 1. 核心重建API（供外部调用）

```go
// RebuildConnectionByID 重建指定ID的连接
// 参数：connID - 连接ID
// 返回：错误信息
func (p *EnhancedConnectionPool) RebuildConnectionByID(connID string) error {
    // 1. 根据connID查找连接
    conn := p.findConnectionByID(connID)
    if conn == nil {
        return fmt.Errorf("连接不存在: %s", connID)
    }
    
    // 2. 检查连接状态是否适合重建
    if !p.checkConnectionStateForRebuild(conn) {
        return fmt.Errorf("连接状态不适合重建: %s, state=%s", connID, conn.state)
    }
    
    // 3. 执行重建
    return p.rebuildConnection(conn.protocol, conn)
}

// RebuildConnectionByProto 重建指定协议下的所有需要重建的连接
// 参数：proto - 协议类型
// 返回：重建的连接数量，错误信息
func (p *EnhancedConnectionPool) RebuildConnectionByProto(proto Protocol) (int, error) {
    // 1. 获取需要重建的连接
    conns := p.getConnectionsForRebuild(proto)
    if len(conns) == 0 {
        return 0, nil
    }
    
    // 2. 执行批量重建
    successCount := 0
    var lastError error
    
    for _, conn := range conns {
        if err := p.rebuildConnection(proto, conn); err != nil {
            lastError = err
            ylog.Warnf("pool", "重建连接失败: id=%s, error=%v", conn.id, err)
        } else {
            successCount++
        }
    }
    
    // 3. 记录结果
    ylog.Infof("pool", "协议 %s 重建完成: 成功 %d/%d", proto, successCount, len(conns))
    
    if lastError != nil && successCount == 0 {
        return 0, fmt.Errorf("所有连接重建失败，最后一个错误: %w", lastError)
    }
    
    return successCount, nil
}

// RebuildConnections 重建所有需要重建的连接
// 返回：各协议重建数量映射，错误信息
func (p *EnhancedConnectionPool) RebuildConnections() (map[Protocol]int, error) {
    protocols := p.getAllProtocols()
    if len(protocols) == 0 {
        return nil, nil
    }
    
    results := make(map[Protocol]int)
    var errors []error
    
    // 并发执行各协议的重建
    var wg sync.WaitGroup
    var mu sync.Mutex
    
    for _, proto := range protocols {
        wg.Add(1)
        go func(proto Protocol) {
            defer wg.Done()
            
            count, err := p.RebuildConnectionByProto(proto)
            
            mu.Lock()
            results[proto] = count
            if err != nil {
                errors = append(errors, fmt.Errorf("协议 %s: %w", proto, err))
            }
            mu.Unlock()
        }(proto)
    }
    
    wg.Wait()
    
    // 汇总错误
    if len(errors) > 0 {
        var errStrs []string
        for _, err := range errors {
            errStrs = append(errStrs, err.Error())
        }
        return results, fmt.Errorf("重建过程中发生错误: %s", strings.Join(errStrs, "; "))
    }
    
    return results, nil
}
```

### 2. 内部辅助API

```go
// 获取需要重建的连接列表
func (p *EnhancedConnectionPool) getConnectionsForRebuild(proto Protocol) []*EnhancedPooledConnection {
    p.mu.RLock()
    defer p.mu.RUnlock()
    
    pool := p.pools[proto]
    if pool == nil {
        return nil
    }
    
    var conns []*EnhancedPooledConnection
    for _, conn := range pool.connections {
        if p.shouldRebuildConnection(conn) {
            conns = append(conns, conn)
        }
    }
    
    return conns
}

// 根据ID查找连接
func (p *EnhancedConnectionPool) findConnectionByID(connID string) *EnhancedPooledConnection {
    p.mu.RLock()
    defer p.mu.RUnlock()
    
    for _, pool := range p.pools {
        for _, conn := range pool.connections {
            if conn.id == connID {
                return conn
            }
        }
    }
    
    return nil
}
```

## 核心重建逻辑实现

### 1. 重建状态管理

```go
// 在 state_manager.go 中添加重建状态
const (
    // ... 现有状态
    StateRebuilding ConnectionState = iota + 6 // 重建中
)

// 在 pooled_connection.go 中添加重建方法
func (conn *EnhancedPooledConnection) beginRebuild() bool {
    conn.mu.Lock()
    defer conn.mu.Unlock()
    
    ylog.Infof("EnhancedPooledConnection", "beginRebuild: 尝试开始重建, id=%s, state=%s", conn.id, conn.state)
    
    // 只有空闲或使用中的连接才能开始重建
    if conn.state != StateIdle && conn.state != StateAcquired {
        ylog.Infof("EnhancedPooledConnection", "beginRebuild: 状态不适合重建, id=%s, state=%s", conn.id, conn.state)
        return false
    }
    
    // 转换为重建中状态
    if !conn.transitionStateLocked(StateRebuilding) {
        ylog.Warnf("EnhancedPooledConnection", "beginRebuild: 状态转换失败, id=%s, from=%s, to=%s", conn.id, conn.state, StateRebuilding)
        return false
    }
    
    ylog.Infof("EnhancedPooledConnection", "beginRebuild: 成功开始重建, id=%s", conn.id)
    return true
}

func (conn *EnhancedPooledConnection) completeRebuild(success bool) {
    conn.mu.Lock()
    defer conn.mu.Unlock()
    
    if conn.state != StateRebuilding {
        ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 连接不在重建中: id=%s, state=%s", conn.id, conn.state)
        return
    }
    
    var targetState ConnectionState
    if success {
        targetState = StateIdle
        conn.lastRebuiltAt = time.Now()
        conn.consecutiveFailures = 0
        conn.healthStatus = HealthStatusHealthy
        conn.usageCount = 0 // 重置使用计数
        ylog.Infof("EnhancedPooledConnection", "completeRebuild: 重建成功, id=%s", conn.id)
    } else {
        targetState = StateClosing
        ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 重建失败, id=%s", conn.id)
    }
    
    if !conn.transitionStateLocked(targetState) {
        ylog.Errorf("EnhancedPooledConnection", "completeRebuild: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, targetState)
        // 强制转换到Closing状态
        conn.state = StateClosing
    }
    
    // 清除重建标记
    atomic.StoreInt32(&conn.markedForRebuild, 0)
}
```

### 2. 核心重建函数

```go
// rebuildConnection 重建连接（核心逻辑）
func (p *EnhancedConnectionPool) rebuildConnection(proto Protocol, conn *EnhancedPooledConnection) error {
    connID := conn.id
    oldDriver := conn.driver
    
    ylog.Infof("pool", "开始重建连接: id=%s, protocol=%s", connID, proto)
    
    startTime := time.Now()
    
    // 1. 标记连接为重建中
    if !conn.beginRebuild() {
        ylog.Warnf("pool", "无法开始重建: connection %s", connID)
        p.collector.IncrementRebuildFailed(proto)
        return fmt.Errorf("无法开始重建连接")
    }
    
    // 2. 创建新连接（带超时）
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    
    newDriver, err := p.factories[proto].CreateDriver(ctx, p.config)
    if err != nil {
        ylog.Errorf("pool", "创建新连接失败: id=%s, error=%v", connID, err)
        conn.completeRebuild(false)
        p.collector.IncrementRebuildFailed(proto)
        p.sendEvent(EventRebuildFailed, proto, map[string]interface{}{
            "connection_id": connID,
            "error":         err.Error(),
            "reason":        "create_failed",
        })
        return fmt.Errorf("创建新连接失败: %w", err)
    }
    
    // 3. 替换连接
    if err := p.replaceConnection(proto, connID, newDriver); err != nil {
        ylog.Errorf("pool", "替换连接失败: id=%s, error=%v", connID, err)
        newDriver.Close()
        conn.completeRebuild(false)
        p.collector.IncrementRebuildFailed(proto)
        p.sendEvent(EventRebuildFailed, proto, map[string]interface{}{
            "connection_id": connID,
            "error":         err.Error(),
            "reason":        "replace_failed",
        })
        return fmt.Errorf("替换连接失败: %w", err)
    }
    
    // 4. 异步关闭旧连接
    go func() {
        if oldDriver != nil {
            oldDriver.Close()
            ylog.Debugf("pool", "旧连接已关闭: id=%s", connID)
        }
    }()
    
    // 5. 完成重建
    conn.completeRebuild(true)
    
    // 6. 记录指标和事件
    duration := time.Since(startTime)
    p.collector.IncrementRebuildSuccess(proto)
    p.collector.RecordRebuildDuration(proto, duration)
    
    reason := p.rebuildManager.GetRebuildReason(conn)
    p.sendEvent(EventConnectionRebuilt, proto, map[string]interface{}{
        "old_connection_id": connID,
        "new_connection_id": conn.id,
        "reason":           reason,
        "duration":         duration.Seconds(),
    })
    
    ylog.Infof("pool", "连接重建完成: id=%s, protocol=%s, reason=%s, duration=%v", 
        connID, proto, reason, duration)
    
    return nil
}

// replaceConnection 替换连接
func (p *EnhancedConnectionPool) replaceConnection(proto Protocol, connID string, newDriver ProtocolDriver) error {
    p.mu.Lock()
    defer p.mu.Unlock()
    
    pool := p.pools[proto]
    if pool == nil {
        return fmt.Errorf("协议池不存在: %s", proto)
    }
    
    // 查找并替换连接
    for i, conn := range pool.connections {
        if conn.id == connID {
            // 创建新的连接包装器
            newConn := &EnhancedPooledConnection{
                driver:     newDriver,
                id:         conn.id, // 保持相同ID
                protocol:   proto,
                createdAt:  time.Now(),
                lastUsed:   time.Now(),
                usageCount: 0,
                state:      StateRebuilding,
                healthStatus: HealthStatusHealthy,
                labels:     conn.labels,
                metadata:   conn.metadata,
            }
            
            // 替换连接
            pool.connections[i] = newConn
            ylog.Debugf("pool", "连接已替换: id=%s", connID)
            return nil
        }
    }
    
    return fmt.Errorf("连接不存在: %s", connID)
}
```

## 定时重建任务实现

```go
// rebuildTask 重建任务（仿照 healthCheckTask）
func (p *EnhancedConnectionPool) rebuildTask() {
    rebuildInterval := p.config.RebuildCheckInterval
    if rebuildInterval == 0 {
        rebuildInterval = 5 * time.Minute // 默认5分钟
    }
    
    ylog.Infof("pool", "重建任务启动，检查间隔: %v", rebuildInterval)
    
    ticker := time.NewTicker(rebuildInterval)
    defer ticker.Stop()

    for {
        select {
        case <-p.parentCtx.Done(): // 监听父级上下文
            ylog.Infof("pool", "重建任务停止（父级上下文取消）")
            return
        case <-p.ctx.Done():
            ylog.Infof("pool", "重建任务停止（上下文取消）")
            return
        case <-ticker.C:
            ylog.Debugf("pool", "定时重建检查触发")
            p.performRebuilds()
        }
    }
}
```

## 指标系统扩展

```go
// 在 metrics.go 的 MetricsCollector 接口中添加
type MetricsCollector interface {
    // ... 现有指标
    
    // 重建指标
    IncrementRebuildSuccess(protocol Protocol)
    IncrementRebuildFailed(protocol Protocol)
    RecordRebuildDuration(protocol Protocol, duration time.Duration)
    SetRebuildQueueSize(protocol Protocol, count int64)
    
    // 连接状态指标
    SetRebuildingConnections(protocol Protocol, count int64)
}

// 在具体的指标收集器实现中添加对应方法
type DefaultMetricsCollector struct {
    // ... 现有字段
    
    // 重建指标
    rebuildSuccess   map[Protocol]int64
    rebuildFailed    map[Protocol]int64
    rebuildDuration  map[Protocol]time.Duration
    rebuildQueueSize map[Protocol]int64
    rebuildingConns  map[Protocol]int64
    
    mu sync.RWMutex
}

func (c *DefaultMetricsCollector) IncrementRebuildSuccess(proto Protocol) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.rebuildSuccess[proto]++
}

func (c *DefaultMetricsCollector) IncrementRebuildFailed(proto Protocol) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.rebuildFailed[proto]++
}

func (c *DefaultMetricsCollector) RecordRebuildDuration(proto Protocol, duration time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.rebuildDuration[proto] = duration
}

func (c *DefaultMetricsCollector) SetRebuildQueueSize(proto Protocol, count int64) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.rebuildQueueSize[proto] = count
}

func (c *DefaultMetricsCollector) SetRebuildingConnections(proto Protocol, count int64) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.rebuildingConns[proto] = count
}
```

## 事件系统扩展

```go
// 在 pool_enhanced.go 的事件枚举中添加
const (
    // ... 现有事件
    
    // 重建相关事件
    EventRebuildStarted PoolEventType = iota + 10
    EventRebuildCompleted
    EventRebuildFailed
    EventRebuildScheduled
)

// 在事件处理函数中添加对应case
func (p *EnhancedConnectionPool) eventHandlerTask() {
    for event := range p.eventChan {
        var eventStr string
        
        switch event.Type {
        // ... 现有case
        
        case EventRebuildStarted:
            if data, ok := event.Data.(map[string]interface{}); ok {
                eventStr = fmt.Sprintf("重建开始: protocol=%s, conn_id=%v",
                    event.Protocol, data["connection_id"])
            }
        case EventRebuildCompleted:
            if data, ok := event.Data.(map[string]interface{}); ok {
                eventStr = fmt.Sprintf("重建完成: protocol=%s, conn_id=%v, duration=%vs",
                    event.Protocol, data["connection_id"], data["duration"])
            }
        case EventRebuildFailed:
            if data, ok := event.Data.(map[string]interface{}); ok {
                eventStr = fmt.Sprintf("重建失败: protocol=%s, conn_id=%v, error=%v",
                    event.Protocol, data["connection_id"], data["error"])
            }
        case EventRebuildScheduled:
            if data, ok := event.Data.(map[string]interface{}); ok {
                eventStr = fmt.Sprintf("重建计划: protocol=%s, count=%v",
                    event.Protocol, data["count"])
            }
        }
        
        ylog.Infof("PoolEvent", "%s (timestamp: %s)", eventStr, event.Timestamp.Format("2006-01-02 15:04:05.000"))
    }
}
```

## 实施步骤（分阶段）

### 第一阶段：基础架构和配置（1-2天）
1. **扩展配置**：在 `EnhancedConnectionConfig` 中添加新增的重建配置
2. **添加重建状态**：在状态机中添加 `StateRebuilding` 状态
3. **实现基础方法**：`beginRebuild()`, `completeRebuild()`
4. **扩展指标接口**：在 `MetricsCollector` 中添加重建指标方法
5. **扩展事件系统**：添加重建相关事件类型

### 第二阶段：核心逻辑实现（2-3天）
1. **实现定时任务**：`rebuildTask()` 和 `performRebuilds()`
2. **实现协议级检查**：`performRebuildForProtocol()`
3. **实现核心重建**：`rebuildConnection()` 和 `replaceConnection()`
4. **实现辅助函数**：`getConnectionsForRebuild()`, `findConnectionByID()`

### 第三阶段：API实现和测试（2-3天）
1. **实现外部API**：`RebuildConnectionByID()`, `RebuildConnectionByProto()`, `RebuildConnections()`
2. **编写单元测试**：测试各个API和核心逻辑
3. **集成测试**：测试完整重建流程
4. **性能测试**：测试重建对系统性能的影响

### 第四阶段：后续集成（稳定后）
1. **修改健康检查**：在 `recordHealthCheck()` 中添加重建触发逻辑
2. **配置健康检查触发**：启用 `HealthCheckTriggerRebuild`
3. **完善错误处理**：添加更复杂的错误恢复机制
4. **优化性能**：根据测试结果进行优化

## 关键修正点说明

### 1. 修复设计矛盾
**问题**：当前 `RebuildManager.checkConnectionHealth()` 在不健康时返回 `false`，跳过重建。
**修正**：不健康的连接应该触发重建，而不是跳过。在后续集成阶段，健康检查失败达到阈值将标记连接需要重建。

### 2. 分阶段实施策略
- **第一阶段**：保持健康检查不动，只实现后台定时重建
- **第二阶段**：实现完整的重建API供外部调用
- **第三阶段**：测试和优化
- **后续阶段**：修改健康检查代码，调用重建API

### 3. 配置设计原则
- 保持向后兼容性
- 新增配置有合理的默认值
- 配置验证和错误处理
- 分阶段启用功能（如 `HealthCheckTriggerRebuild` 默认关闭）

### 4. 错误处理和恢复
- 重建失败时记录详细错误信息
- 失败连接标记为关闭状态
- 异步清理旧连接资源
- 指标记录成功/失败次数

### 5. 性能考虑
- 控制并发重建数量
- 批量处理避免资源冲击
- 重建超时控制
- 异步操作减少阻塞

## 预期效果

### 短期效果（第一阶段完成后）
1. **完整的定时重建机制**：类似健康检查的定时触发
2. **外部调用API**：支持手动触发重建
3. **基础监控**：重建指标和事件通知
4. **资源友好**：控制并发，避免影响正常业务

### 长期效果（全部阶段完成后）
1. **自动恢复能力**：健康检查失败自动触发重建
2. **智能决策**：基于多种条件智能判断重建时机
3. **完整生命周期管理**：健康检查监控 + 重建恢复
4. **高可观测性**：完整的监控、指标和事件系统

## 注意事项

1. **并发安全**：确保同一连接不会被多个goroutine同时重建
2. **资源限制**：控制并发重建数量，避免资源耗尽
3. **错误隔离**：单个连接重建失败不影响其他连接
4. **状态一致性**：重建过程中保持连接状态一致
5. **监控告警**：重建失败时应有告警机制

---
*文档修正时间：2025-12-25*
*基于代码分析修正：修复设计矛盾，分阶段实施*
*设计目标：先实现后台定时重建，后集成健康检查触发*
