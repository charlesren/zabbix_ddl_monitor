# 健康检查与连接重建逻辑设计文档

## 文档概述

本文档详细描述了连接的健康检查机制、状态管理系统和连接重建逻辑的完整设计。基于对现有代码的分析，提出了优化方案和实施步骤。

## 1. 核心概念定义

### 1.1 连接状态（ConnectionState）
连接的操作生命周期状态，定义在 `state_manager.go`：

```go
type ConnectionState int
const (
    StateIdle ConnectionState = iota      // 0: 空闲状态（可用）
    StateConnecting                       // 1: 连接中状态（正在建立连接）
    StateAcquired                         // 2: 已获取状态（使用中）
    StateExecuting                        // 3: 执行中状态（正在执行命令）
    StateChecking                         // 4: 检查状态（健康检查中）
    StateRebuilding                       // 5: 重建状态（正在重建）
    StateClosing                          // 6: 关闭中状态
    StateClosed                           // 7: 已关闭状态（终止状态）
)
```

### 1.2 健康状态（HealthStatus）
连接的健康质量状态，定义在 `health_manager.go`：

```go
type HealthStatus int
const (
    HealthStatusUnknown  HealthStatus = iota  // 0: 未知状态（初始状态）
    HealthStatusHealthy                       // 1: 健康状态（完全正常）
    HealthStatusDegraded                      // 2: 降级状态（可用但性能下降）
    HealthStatusUnhealthy                     // 3: 不健康状态（不可用）
)
```

### 1.3 重建标记（markedForRebuild）
连接的重建需求标记，定义在 `EnhancedPooledConnection` 结构体：

```go
type EnhancedPooledConnection struct {
    // ...
    markedForRebuild int32  // 0=不需要重建, 1=需要重建（已决策）
    // ...
}
```

## 2. 状态转换逻辑

### 2.1 健康状态转换规则

#### **健康检查结果分类**
```go
func classifyHealthCheck(duration time.Duration, err error, consecutiveFailures int) HealthStatus {
    if err != nil {
        // 检查失败 -> 不健康
        return HealthStatusUnhealthy
    }
    
    if duration > degradedResponseTimeThreshold {
        // 响应时间过长 -> 降级
        return HealthStatusDegraded
    }
    
    // 正常 -> 健康
    return HealthStatusHealthy
}
```

#### **连续失败次数与状态关系**
| 连续失败次数 | 健康状态 | 说明 |
|-------------|----------|------|
| 0 | Healthy | 完全正常 |
| 1 | Degraded | 首次失败，性能降级 |
| 2 | Degraded | 连续失败，保持降级 |
| ≥3 | Unhealthy | 连续失败达到阈值，不健康 |

#### **状态转换矩阵**
```
Unknown → Healthy:    首次健康检查成功
Unknown → Unhealthy:  首次健康检查失败

Healthy → Degraded:   健康检查响应时间过长或首次失败
Healthy → Unhealthy:  健康检查完全失败

Degraded → Healthy:   健康检查恢复正常
Degraded → Unhealthy: 连续失败达到阈值（默认3次）

Unhealthy → Healthy:  重建成功或手动恢复
```

### 2.2 连接状态转换规则

#### **状态转换验证函数**
```go
func CanTransition(currentState, targetState ConnectionState) bool {
    // 严格的有限状态机转换规则
    // 详见 state_manager.go
}
```

#### **关键转换路径**
1. **正常使用流程**：`Idle → Acquired → Executing → Acquired → Idle`
2. **健康检查流程**：`Idle → Checking → Idle`
3. **重建流程**：`Idle → Rebuilding → Idle`
4. **关闭流程**：`Any → Closing → Closed`

### 2.3 重建标记生命周期

#### **标记设置条件**
```go
func shouldMarkForRebuild(conn *EnhancedPooledConnection) bool {
    // 条件1：健康状态为Unhealthy且连续失败≥阈值
    if conn.healthStatus == HealthStatusUnhealthy &&
       conn.consecutiveFailures >= unhealthyThreshold {
        return true
    }
    
    // 条件2：健康状态为Degraded且配置允许
    if conn.healthStatus == HealthStatusDegraded &&
       conn.config.RebuildOnDegraded {
        return true
    }
    
    // 条件3：其他重建策略（使用次数、年龄、错误率）
    // 在RebuildManager中处理
    
    return false
}
```

#### **标记生命周期**
```
0 (初始) → 1 (需要重建) → 保持1 (重建中) → 0 (重建完成)
      ↓           ↓             ↓             ↓
   (决策阶段)  (排队阶段)    (执行阶段)    (完成阶段)
```

## 3. 健康检查机制

### 3.1 健康检查配置

```go
type EnhancedConnectionConfig struct {
    // 基础配置
    HealthCheckTime    time.Duration `json:"health_check_time" yaml:"health_check_time"`     // 检查间隔，默认30秒
    HealthCheckTimeout time.Duration `json:"health_check_timeout" yaml:"health_check_timeout"` // 检查超时，默认5秒
    
    // 状态阈值配置
    UnhealthyThreshold int           `json:"unhealthy_threshold" yaml:"unhealthy_threshold"`   // 不健康阈值，默认3次
    DegradedThreshold  time.Duration `json:"degraded_threshold" yaml:"degraded_threshold"`     // 降级响应时间阈值
    
    // 触发重建配置
    HealthCheckTriggerRebuild bool `json:"health_check_trigger_rebuild" yaml:"health_check_trigger_rebuild"` // 是否触发重建
    RebuildOnDegraded         bool `json:"rebuild_on_degraded" yaml:"rebuild_on_degraded"`                   // 降级时是否重建
}
```

### 3.2 健康检查执行流程

```
healthCheckTask() [定时器，默认30秒]
    ↓
performHealthChecks()
    ↓
performHealthCheckForProtocol(proto) [每个协议]
    ↓
p.GetWithContext(ctx, proto) [池API获取连接]
    ↓
defaultHealthCheck() [执行检查]
    ↓
recordHealthCheck() [记录结果]
    ↓
p.Release(driver) [释放连接]
```

### 3.3 健康检查结果处理

```go
func (conn *EnhancedPooledConnection) recordHealthCheck(duration time.Duration, err error) {
    conn.mu.Lock()
    defer conn.mu.Unlock()
    
    conn.lastHealthCheck = time.Now()
    conn.totalRequests++
    
    if err != nil {
        // 检查失败
        conn.consecutiveFailures++
        conn.totalErrors++
        
        // 根据连续失败次数设置健康状态
        if conn.consecutiveFailures >= conn.config.UnhealthyThreshold {
            conn.healthStatus = HealthStatusUnhealthy
            // 标记需要重建
            if conn.config.HealthCheckTriggerRebuild {
                conn.markForRebuildWithReason(fmt.Sprintf("health_failures_%d", conn.consecutiveFailures))
            }
        } else if conn.consecutiveFailures >= 1 {
            conn.healthStatus = HealthStatusDegraded
        }
    } else if duration > conn.config.DegradedThreshold {
        // 响应时间过长 -> 降级
        conn.healthStatus = HealthStatusDegraded
        conn.consecutiveFailures = 0
        
        // 降级时可能触发重建
        if conn.config.RebuildOnDegraded {
            conn.markForRebuildWithReason("degraded_performance")
        }
    } else {
        // 检查成功
        conn.healthStatus = HealthStatusHealthy
        conn.consecutiveFailures = 0
    }
    
    // 确保状态恢复为Idle
    if conn.state == StateChecking {
        conn.transitionStateLocked(StateIdle)
    }
}
```

## 4. 重建机制

### 4.1 重建配置

```go
type EnhancedConnectionConfig struct {
    // 智能重建基础配置
    SmartRebuildEnabled            bool          `json:"smart_rebuild_enabled" yaml:"smart_rebuild_enabled"`
    RebuildMaxUsageCount           int64         `json:"rebuild_max_usage_count" yaml:"rebuild_max_usage_count"`           // 默认200
    RebuildMaxAge                  time.Duration `json:"rebuild_max_age" yaml:"rebuild_max_age"`                           // 默认30分钟
    RebuildMaxErrorRate            float64       `json:"rebuild_max_error_rate" yaml:"rebuild_max_error_rate"`             // 默认0.2 (20%)
    RebuildMinInterval             time.Duration `json:"rebuild_min_interval" yaml:"rebuild_min_interval"`                 // 默认5分钟
    RebuildMinRequestsForErrorRate int64         `json:"rebuild_min_requests_for_error_rate" yaml:"rebuild_min_requests_for_error_rate"` // 默认10
    RebuildStrategy                string        `json:"rebuild_strategy" yaml:"rebuild_strategy"`                         // "any"|"all"|"usage"|"age"|"error"
    
    // 新增重建执行配置
    RebuildCheckInterval   time.Duration `json:"rebuild_check_interval" yaml:"rebuild_check_interval"`   // 重建检查间隔，默认5分钟
    RebuildBatchSize       int           `json:"rebuild_batch_size" yaml:"rebuild_batch_size"`           // 批量重建大小，默认5
    RebuildConcurrency     int           `json:"rebuild_concurrency" yaml:"rebuild_concurrency"`         // 并发重建数，默认3
}
```

### 4.2 重建决策逻辑

#### **重建条件检查顺序**
```go
func (rm *RebuildManager) ShouldRebuild(conn *EnhancedPooledConnection) bool {
    // 1. 检查重建是否启用
    if !rm.checkRebuildEnabled() { return false }
    
    // 2. 检查重建间隔
    if !rm.checkRebuildInterval(conn, time.Now()) { return false }
    
    // 3. 检查连接状态是否适合重建
    if !rm.checkConnectionState(conn) { return false }
    
    // 4. 检查是否已标记重建（防止并发）
    if !rm.checkRebuildMark(conn) { return false }
    
    // 5. 检查健康状态（修改：不健康状态不影响重建决策）
    // if !rm.checkConnectionHealth(conn) { return false } // 移除或修改
    
    // 6. 根据策略评估
    return rm.evaluateRebuildStrategy(conn, time.Now())
}
```

#### **健康状态检查修改**
```go
// 修改前：不健康状态跳过重建（错误逻辑）
func (rm *RebuildManager) checkConnectionHealth(conn *EnhancedPooledConnection) bool {
    if !conn.isHealthy() {
        return false  // 错误：不健康应该重建，而不是跳过
    }
    return true
}

// 修改后：健康状态不影响重建决策
func (rm *RebuildManager) checkConnectionHealth(conn *EnhancedPooledConnection) bool {
    // 总是返回true，让其他条件决定是否重建
    // 实际上，不健康状态更应该重建
    return true
}
```

### 4.3 重建执行流程

#### **三层重建架构**
```
rebuildTask() [定时器，默认5分钟]
    ↓
performRebuilds()
    ↓
performRebuildForProtocol(proto) [每个协议]
    ↓
rebuildConnection(proto, conn) [每个连接]
```

#### **核心重建函数**
```go
func (p *EnhancedConnectionPool) rebuildConnection(proto Protocol, conn *EnhancedPooledConnection) error {
    // 1. 标记连接为重建中
    if !conn.beginRebuild() { return fmt.Errorf("无法开始重建") }
    
    // 2. 创建新连接
    newDriver, err := p.createNewDriver(proto)
    if err != nil { return err }
    
    // 3. 替换连接
    if err := p.replaceConnection(proto, conn.id, newDriver); err != nil {
        newDriver.Close()
        return err
    }
    
    // 4. 异步关闭旧连接
    go conn.driver.Close()
    
    // 5. 完成重建
    conn.completeRebuild(true)
    
    // 6. 记录指标和事件
    p.recordRebuildSuccess(proto, conn)
    
    return nil
}
```

### 4.4 重建API设计

#### **外部调用API**
```go
// 重建指定ID的连接
func (p *EnhancedConnectionPool) RebuildConnectionByID(connID string) error

// 重建指定协议下的所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnectionByProto(proto Protocol) (int, error)

// 重建所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnections() (map[Protocol]int, error)
```

#### **内部辅助API**
```go
// 获取需要重建的连接列表
func (p *EnhancedConnectionPool) GetConnectionsNeedingRebuild(proto Protocol) []*EnhancedPooledConnection

// 检查连接是否需要重建
func (conn *EnhancedPooledConnection) needsRebuild() bool
```

## 5. 字段说明与数据结构

### 5.1 EnhancedPooledConnection 关键字段

```go
type EnhancedPooledConnection struct {
    // 连接级读写锁
    mu sync.RWMutex
    
    // 连接状态（生命周期）
    state ConnectionState
    
    // 健康状态（质量状态）
    healthStatus HealthStatus
    
    // 重建标记（并发控制）
    markedForRebuild int32  // 0=不需要, 1=需要重建
    
    // 健康检查相关
    lastHealthCheck     time.Time  // 最后健康检查时间
    consecutiveFailures int        // 连续失败次数
    totalRequests       int64      // 总请求数
    totalErrors         int64      // 总错误数
    
    // 使用统计
    usageCount  int64      // 使用次数
    createdAt   time.Time  // 创建时间
    lastUsed    time.Time  // 最后使用时间
    lastRebuiltAt time.Time // 最后重建时间
    
    // 性能指标
    avgResponseTime time.Duration  // 平均响应时间
    
    // 标签和元数据
    labels   map[string]string
    metadata map[string]interface{}
    
    // 重建原因（便于监控）
    rebuildReason string
}
```

### 5.2 状态查询方法

```go
// 获取完整状态
func (conn *EnhancedPooledConnection) getStatus() (state ConnectionState, health HealthStatus)

// 检查连接是否可用
func (conn *EnhancedPooledConnection) isAvailable() bool {
    return conn.state == StateIdle && conn.healthStatus != HealthStatusUnhealthy
}

// 检查连接是否健康（Degraded被认为是健康的）
func (conn *EnhancedPooledConnection) isHealthy() bool {
    return conn.healthStatus != HealthStatusUnhealthy
}

// 检查连接是否需要重建
func (conn *EnhancedPooledConnection) needsRebuild() bool {
    // 已标记需要重建
    if conn.isMarkedForRebuild() {
        return true
    }
    
    // 不健康且达到阈值
    if conn.healthStatus == HealthStatusUnhealthy &&
       conn.consecutiveFailures >= conn.config.UnhealthyThreshold {
        return true
    }
    
    // 降级且配置允许
    if conn.healthStatus == HealthStatusDegraded &&
       conn.config.RebuildOnDegraded {
        return true
    }
    
    return false
}
```

## 6. 监控与可观测性

### 6.1 指标收集接口

```go
type MetricsCollector interface {
    // 健康状态指标
    SetConnectionHealthStatus(protocol Protocol, status HealthStatus, count int64)
    IncrementHealthCheckSuccess(protocol Protocol)
    IncrementHealthCheckFailed(protocol Protocol)
    RecordHealthCheckDuration(protocol Protocol, duration time.Duration)
    
    // 重建指标
    SetConnectionsNeedingRebuild(protocol Protocol, count int64)
    IncrementRebuildMarked(protocol Protocol, reason string)
    IncrementRebuildStarted(protocol Protocol)
    IncrementRebuildCompleted(protocol Protocol)
    IncrementRebuildFailed(protocol Protocol)
    RecordRebuildDuration(protocol Protocol, duration time.Duration)
    
    // 连接池指标
    SetActiveConnections(protocol Protocol, count int64)
    SetIdleConnections(protocol Protocol, count int64)
    SetPoolSize(protocol Protocol, count int64)
}
```

### 6.2 事件通知系统

#### **事件类型**
```go
const (
    // 健康检查事件
    EventHealthCheckFailed PoolEventType = iota + 5
    EventHealthStatusChanged
    EventHealthCheckTriggeredRebuild
    
    // 重建事件
    EventRebuildMarked
    EventRebuildStarted
    EventRebuildCompleted
    EventRebuildFailed
    EventConnectionRebuilt
)
```

#### **事件数据格式**
```go
// 健康状态变化事件
{
    "connection_id": "conn-123",
    "old_status": "healthy",
    "new_status": "degraded",
    "failures": 1,
    "timestamp": "2025-12-25T10:30:00Z"
}

// 重建标记事件
{
    "connection_id": "conn-123",
    "reason": "health_failures_3",
    "health_status": "unhealthy",
    "timestamp": "2025-12-25T10:30:00Z"
}

// 重建完成事件
{
    "old_connection_id": "conn-123",
    "new_connection_id": "conn-124",
    "reason": "health_failures_3",
    "duration": 2.5,  // 秒
    "timestamp": "2025-12-25T10:30:02Z"
}
```

## 7. 需要修改的步骤清单

### 第一阶段：健康检查逻辑修改（1-2天）

#### **7.1 修改健康状态检查逻辑**
1. 修改 `RebuildManager.checkConnectionHealth()` 方法
   - 移除或修改健康状态检查，不阻止不健康连接重建
   - 改为总是返回true，或添加日志但不阻止

2. 修改 `recordHealthCheck()` 方法
   - 根据连续失败次数设置 `Degraded` 和 `Unhealthy` 状态
   - 添加连续失败次数阈值配置
   - 达到阈值时自动标记重建

#### **7.2 添加降级状态支持**
1. 添加 `DegradedThreshold` 配置参数
2. 修改健康检查分类逻辑
3. 添加 `RebuildOnDegraded` 配置选项

#### **7.3 完善健康检查指标**
1. 添加健康状态分布指标
2. 添加连续失败次数监控
3. 添加健康状态变化事件

### 第二阶段：重建机制完善（2-3天）

#### **7.4 修复重建标记逻辑**
1. 修改 `beginRebuildWithLock()` 方法
   - 检查 `markedForRebuild` 标记
   - 保持标记不清除，直到重建完成

2. 修改 `completeRebuild()` 方法
   - 重建完成后清除标记
   - 重置健康状态和连续失败次数

#### **7.5 实现定时重建任务**
1. 添加 `rebuildTask()` 定时任务
2. 在 `startBackgroundTasks()` 中启动
3. 实现三层重建架构

#### **7.6 实现重建API**
1. 实现 `RebuildConnectionByID()` API
2. 实现 `RebuildConnectionByProto()` API
3. 实现 `RebuildConnections()` API

### 第三阶段：配置和监控完善（1-2天）

#### **7.7 扩展配置结构**
1. 在 `EnhancedConnectionConfig` 中添加新配置
2. 设置合理的默认值
3. 添加配置验证

#### **7.8 完善监控指标**
1. 扩展 `MetricsCollector` 接口
2. 实现新的指标收集方法
3. 添加重建相关事件

#### **7.9 添加测试用例**
1. 健康检查状态转换测试
2. 重建标记逻辑测试
3. 重建API集成测试

### 第四阶段：性能优化和文档（1天）

#### **7.10 性能优化**
1. 优化重建并发控制
2. 添加批量重建支持
3. 优化锁竞争

#### **7.11 文档完善**
1. 更新设计文档
2. 添加API使用文档
3. 添加配置说明文档

## 8. 关键设计决策总结

### 8.1 健康状态设计
- **`Healthy`**：完全正常，无需干预
- **`Degraded`**：可用但性能下降，**可能**需要重建（基于配置）
- **`Unhealthy`**：不可用，**应该**重建（达到阈值时自动标记）
- **`Unknown`**：初始状态，无历史数据

### 8.2 重建触发条件
1. **已标记重建** (`markedForRebuild=1`)：最高优先级
2. **不健康状态** + 连续失败≥阈值：自动标记
3. **降级状态** + 配置允许：可选标记
4. **其他策略**：使用次数、年龄、错误率

### 8.3 状态分离原则
- **连接状态** (`ConnectionState`)：操作生命周期（**怎么做**）
- **健康状态** (`HealthStatus`)：质量监控（**怎么样**）
- **重建标记** (`markedForRebuild`)：恢复决策（**何时恢复**）

### 8.4 并发控制设计
- `markedForRebuild`：原子操作，轻量级决策阶段并发控制
- `StateRebuilding`：连接锁保护，重量级执行阶段状态管理
- 两者协同：标记 → 检查 → 执行 → 完成 → 清除标记

## 9. 风险与缓解措施

### 9.1 风险：过度重建
- **表现**：连接频繁重建，影响性能
- **缓解**：
  - 设置最小重建间隔 (`RebuildMinInterval`)
  - 控制并发重建数量 (`RebuildConcurrency`)
  - 添加重建冷却期

### 9.2 风险：重建失败累积
- **表现**：连接重建失败，标记不清除，无法再次重建
- **缓解**：
  - 添加重建失败计数
  - 设置最大重建尝试次数
  - 失败达到阈值时关闭连接

### 9.3 风险：状态不一致
- **表现**：健康状态、重建标记、连接状态不一致
- **缓解**：
  - 严格的锁保护
  - 状态转换验证
  - 详细的日志记录

### 9.4 风险：性能影响
- **表现**：健康检查和重建影响正常业务
- **缓解**：
  - 异步执行健康检查和重建
  - 控制检查频率和并发数
  - 添加性能监控和告警

## 10. 后续扩展建议

### 10.1 智能重建策略
- 基于机器学习预测连接故障
- 自适应调整重建阈值
- 优先级队列管理重建任务

### 10.2 高级健康检查
- 多维健康指标（延迟、吞吐量、错误率）
- 自适应检查频率
- 健康检查负载均衡

### 10.3 监控告警集成
- 与现有监控系统集成
- 智能告警规则
- 自动化故障处理工作流

### 10.4 灰度发布支持
- 连接级别的灰度发布
- A/B测试支持
- 流量染色和路由

---
*文档创建时间：2025-12-25*
*基于代码分析版本：zabbix_ddl_monitor*
*设计目标：实现完整的健康检查、状态管理和连接重建机制*
