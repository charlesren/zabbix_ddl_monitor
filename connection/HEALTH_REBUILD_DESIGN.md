# 健康检查与连接重建逻辑设计文档

## 文档概述

本文档详细描述了连接的健康检查机制、状态管理系统和连接重建逻辑的完整设计。

## 1. 核心概念定义

### 1.1 连接状态（ConnectionState）
连接的操作生命周期状态：

```go
type ConnectionState int
const (
    StateIdle ConnectionState = iota      // 0: 空闲状态（可用）
    StateConnecting                       // 1: 连接中状态
    StateAcquired                         // 2: 已获取状态（使用中）
    StateExecuting                        // 3: 执行中状态
    StateChecking                         // 4: 检查状态（健康检查中）
    StateClosing                          // 5: 关闭中状态
    StateClosed                           // 6: 已关闭状态（终止状态）
)
```

### 1.2 健康状态（HealthStatus）
连接的健康质量状态：

```go
type HealthStatus int
const (
    HealthStatusUnknown  HealthStatus = iota  // 0: 未知状态（初始状态）
    HealthStatusHealthy                       // 1: 健康状态（完全正常）
    HealthStatusDegraded                      // 2: 降级状态（可用但性能下降）
    HealthStatusUnhealthy                     // 3: 不健康状态（不可用）
)
```

### 1.3 重建标记与状态管理

连接使用两个字段管理重建：

**`markedForRebuild` (int32)** - 决策阶段标记
- `0`: 未标记，可以被决策为需要重建
- `1`: 已标记，表示已有goroutine决策此连接需要重建
- 使用原子操作，防止并发重复决策

**`isRebuilding` (bool)** - 执行阶段标记
- 表示连接正在执行重建操作
- 需要锁保护，防止并发执行重建

**两阶段并发控制流程**:
```
决策阶段：ShouldRebuild() → 检查markedForRebuild=0 → 标记markedForRebuild=1
验证阶段：beginRebuild() → 检查isRebuilding=false → 设置isRebuilding=true
执行阶段：保持isRebuilding=true执行重建
完成阶段：completeRebuild() → 清除markedForRebuild=0 → 设置isRebuilding=false
```

## 2. 状态转换逻辑

### 2.1 健康状态转换规则

#### 连续失败次数与状态关系
| 连续失败次数 | 健康状态 | 说明 |
|-------------|----------|------|
| 0 | Healthy | 完全正常（初始状态或检查成功后） |
| 1 | Degraded 或 Healthy | 首次失败，取决于 `DegradedFailureThreshold`（默认1） |
| 2 | Degraded 或 Unhealthy | 连续失败，取决于阈值配置 |
| ≥3 | Unhealthy | 连续失败达到 `UnhealthyFailureThreshold`（默认3） |

#### 状态转换
```
Unknown/任何状态 → Healthy: 健康检查成功（consecutiveFailures 重置为0）

Healthy → Degraded: 健康检查失败，且 consecutiveFailures ≥ DegradedFailureThreshold（默认1）
Healthy → Unhealthy: 健康检查失败，且 consecutiveFailures ≥ UnhealthyFailureThreshold（默认3）

Degraded → Healthy: 健康检查成功（consecutiveFailures 重置为0）
Degraded → Unhealthy: 继续失败，consecutiveFailures ≥ UnhealthyFailureThreshold

Unhealthy → Healthy: 健康检查成功（consecutiveFailures 重置为0）
```

**注意**:
- 状态转换完全基于健康检查的成功/失败和连续失败次数
- 没有基于响应时间的判断
- `DegradedFailureThreshold` 默认为 1
- `UnhealthyFailureThreshold` 默认为 3

### 2.2 连接状态转换规则

#### 关键转换路径
1. **正常使用流程**: `Idle → Acquired → Executing → Acquired → Idle`
2. **健康检查流程**: `Idle → Checking → Idle`
3. **关闭流程**: `Any → Closing → Closed`

## 3. 健康检查机制

### 3.1 健康检查分类

```go
func (conn *EnhancedPooledConnection) recordHealthCheck(success bool, err error) {
    conn.mu.Lock()
    defer conn.mu.Unlock()
    
    conn.lastHealthCheck = time.Now()
    
    if success {
        // 健康检查成功
        conn.consecutiveFailures = 0
        conn.setHealthLocked(HealthStatusHealthy, true)
    } else {
        // 健康检查失败
        conn.consecutiveFailures++
        
        degradedThreshold := conn.config.DegradedFailureThreshold   // 默认1
        unhealthyThreshold := conn.config.UnhealthyFailureThreshold // 默认3
        
        if conn.consecutiveFailures >= unhealthyThreshold {
            // 达到不健康阈值
            conn.setHealthLocked(HealthStatusUnhealthy, false)
            
            // 触发重建
            if conn.config.HealthCheckTriggerRebuild {
                reason := fmt.Sprintf("health_check_failed_%d_times", conn.consecutiveFailures)
                conn.markForRebuildWithReasonLocked(reason)
            }
        } else if conn.consecutiveFailures >= degradedThreshold {
            // 达到降级阈值
            conn.setHealthLocked(HealthStatusDegraded, false)
            
            // 可选：降级时也触发重建
            if conn.config.RebuildOnDegraded {
                reason := fmt.Sprintf("health_check_degraded_%d_times", conn.consecutiveFailures)
                conn.markForRebuildWithReasonLocked(reason)
            }
        } else {
            // 低于阈值，仍视为健康
            conn.setHealthLocked(HealthStatusHealthy, false)
        }
    }
}
```

### 3.2 健康检查结果处理

健康检查结果处理遵循以下逻辑：

1. **检查成功**: 
   - 重置 `consecutiveFailures = 0`
   - 设置健康状态为 `HealthStatusHealthy`

2. **检查失败**:
   - 增加 `consecutiveFailures++`
   - 根据 `consecutiveFailures` 判断：
     - `≥ UnhealthyFailureThreshold` (默认3) → `HealthStatusUnhealthy`
     - `≥ DegradedFailureThreshold` (默认1) → `HealthStatusDegraded`
     - `< DegradedFailureThreshold` → `HealthStatusHealthy`（仍视为健康）

3. **触发重建**:
   - 达到 Unhealthy 时，如果 `HealthCheckTriggerRebuild = true`
   - 达到 Degraded 时，如果 `RebuildOnDegraded = true`

## 4. 重建机制

### 4.1 重建策略

```go
type RebuildStrategy string

const (
    RebuildStrategyUsage RebuildStrategy = "usage"  // 基于使用次数
    RebuildStrategyAge   RebuildStrategy = "age"    // 基于连接年龄
    RebuildStrategyError RebuildStrategy = "error"  // 基于错误率
    RebuildStrategyAll   RebuildStrategy = "all"    // 所有条件满足
    RebuildStrategyAny   RebuildStrategy = "any"    // 任意条件满足（默认）
)
```

### 4.2 重建条件

1. **使用次数**: `usageCount >= RebuildMaxUsageCount` (默认200)
2. **连接年龄**: `age >= RebuildMaxAge` (默认30分钟)
3. **错误率**: `errorRate >= RebuildMaxErrorRate` (默认0.2)
4. **最小间隔**: 距上次创建/重建时间 >= `RebuildMinInterval` (默认5分钟)

### 4.3 重建决策流程

```
EvaluateAndMark(conn)
    ↓
1. 检查是否已标记 (快速路径)
    ↓
2. 检查重建是否启用
    ↓
3. 检查重建间隔
    ↓
4. 检查连接状态
    ↓
5. 根据策略评估并标记
    ↓
返回: (是否标记成功, 原因)
```

### 4.4 重建执行流程

```
rebuildConnection(ctx, conn)
    ↓
1. 标记连接为重建中 (beginRebuild)
    ↓
2. 创建新连接
    ↓
3. 替换连接
    ↓
4. 异步关闭旧连接
    ↓
5. 完成重建 (completeRebuild)
    ↓
6. 发送事件和收集指标
```

## 5. 重建API设计

### 5.1 单个连接重建

#### `RebuildConnectionByID(ctx, connID) (*RebuildResult, error)`
同步重建指定ID的单个连接。

**参数**:
- `ctx context.Context` - 支持取消和超时的上下文
- `connID string` - 连接ID

**返回**: `*RebuildResult` - 详细重建结果, `error` - 错误信息

#### `RebuildConnectionByIDAsync(ctx, connID) (<-chan *RebuildResult, error)`
异步重建指定ID的单个连接，返回结果通道。

### 5.2 协议批量重建

#### `RebuildConnectionByProto(ctx, proto) (*BatchRebuildResult, error)`
同步重建指定协议的所有需要重建的连接。

**特点**:
- ⭐ **并发执行**: Worker Pool 模式
- ⭐ **可配置并发度**: `RebuildConcurrency` (默认3)
- ⭐ **批次控制**: `RebuildBatchSize` (默认5)

**性能**: 串行模式需要 10N 秒，并发模式约 3.3N 秒，**提速约3倍**

### 5.3 全量重建

#### `RebuildConnections(ctx) (*BatchRebuildResult, error)`
重建所有协议的连接。

**特点**:
- ⭐ **并发处理所有协议**
- 统一返回 `BatchRebuildResult`
- 通过 `Results` 数组中每个结果的 `Protocol` 字段区分协议

### 5.4 数据结构

**单个连接重建结果**
```go
type RebuildResult struct {
    Protocol   string        `json:"protocol"`
    Success    bool          `json:"success"`
    OldConnID  string        `json:"old_conn_id"`
    NewConnID  string        `json:"new_conn_id,omitempty"`
    Duration   time.Duration `json:"duration"`
    Reason     string        `json:"reason"`
    Error      string        `json:"error,omitempty"`
    Timestamp  time.Time     `json:"timestamp"`
}
```

**批量重建结果**
```go
type BatchRebuildResult struct {
    Total       int              `json:"total"`
    Success     int              `json:"success"`
    Failed      int              `json:"failed"`
    Results     []*RebuildResult `json:"results"`
    StartTime   time.Time        `json:"start_time"`
    EndTime     time.Time        `json:"end_time"`
    Duration    time.Duration    `json:"duration"`
    Errors      []string         `json:"errors"`
    Cancelled   bool             `json:"cancelled"`
    CancelError string           `json:"cancel_error,omitempty"`
}
```

## 6. 监控与可观测性

### 6.1 指标收集接口

```go
type MetricsCollector interface {
    // 连接指标
    IncrementConnectionsCreated(protocol Protocol)
    IncrementConnectionsDestroyed(protocol Protocol)
    IncrementConnectionsReused(protocol Protocol)
    IncrementConnectionsFailed(protocol Protocol)
    
    // 健康检查指标
    IncrementHealthCheckSuccess(protocol Protocol)
    IncrementHealthCheckFailed(protocol Protocol)
    RecordHealthCheckDuration(protocol Protocol, duration time.Duration)
    
    // 重建指标
    IncrementConnectionsNeedingRebuild(protocol Protocol)
    IncrementRebuildMarked(protocol Protocol, reason string)
    IncrementRebuildStarted(protocol Protocol)
    IncrementRebuildCompleted(protocol Protocol)
    IncrementRebuildFailed(protocol Protocol)
    RecordRebuildDuration(protocol Protocol, duration time.Duration)
    SetRebuildingConnections(protocol Protocol, count int64)
    IncrementRebuildingConnections(protocol Protocol)
    DecrementRebuildingConnections(protocol Protocol)
    
    // 连接池指标
    SetActiveConnections(protocol Protocol, count int64)
    SetIdleConnections(protocol Protocol, count int64)
    SetPoolSize(protocol Protocol, count int64)
}
```

### 6.2 事件类型

```go
const (
    // 基础事件
    EventConnectionCreated PoolEventType = iota
    EventConnectionDestroyed
    EventConnectionReused
    EventConnectionFailed
    EventHealthCheckFailed
    EventPoolWarmupStarted
    EventPoolWarmupCompleted
    EventPoolShutdown
    
    // 重建事件
    EventRebuildMarked          // 连接被标记为需要重建
    EventRebuildStarted         // 重建开始
    EventRebuildCompleted       // 重建完成
    EventConnectionRebuilt      // 连接重建完成
    EventRebuildFailed          // 重建失败
)
```

### 6.3 关键性能指标 (KPI)

```go
// 1. 重建成功率
successRate := float64(metrics.RebuildCompleted) / float64(metrics.RebuildStarted)

// 2. 重建失败率
failureRate := float64(metrics.RebuildFailed) / float64(metrics.RebuildStarted)

// 3. 平均重建耗时
avgDuration := metrics.RebuildDuration

// 4. 重建积压
backlog := metrics.ConnectionsNeedingRebuild - metrics.RebuildingConnections

// 5. 并发利用率
utilization := float64(metrics.RebuildingConnections) / float64(config.RebuildConcurrency)

// 6. 连接复用率
reuseRate := float64(metrics.Reused) / float64(metrics.Created + metrics.Reused)
```

## 7. 配置参数

### 7.1 健康检查配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `health_check_time` | time.Duration | 30s | 健康检查间隔 |
| `health_check_timeout` | time.Duration | 5s | 健康检查超时 |
| `degraded_failure_threshold` | int | 1 | 降级阈值（连续失败次数） |
| `unhealthy_failure_threshold` | int | 3 | 不健康阈值（连续失败次数） |
| `health_check_trigger_rebuild` | bool | true | 健康检查失败是否触发重建 |
| `rebuild_on_degraded` | bool | false | 降级时是否触发重建 |

**阈值说明**:
- `degraded_failure_threshold = 1`: 第一次失败即进入 Degraded 状态
- `unhealthy_failure_threshold = 3`: 连续失败3次进入 Unhealthy 状态
- 调整这些值可以改变健康状态转换的敏感度

### 7.2 智能重建配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `smart_rebuild_enabled` | bool | true | 是否启用智能重建 |
| `rebuild_strategy` | string | "any" | 重建策略: any\|all\|usage\|age\|error |
| `rebuild_max_usage_count` | int | 200 | 最大使用次数 |
| `rebuild_max_age` | time.Duration | 30m | 最大连接年龄 |
| `rebuild_max_error_rate` | float64 | 0.2 | 最大错误率 (20%) |
| `rebuild_min_interval` | time.Duration | 5m | 最小重建间隔 |
| `rebuild_min_requests_for_error_rate` | int | 10 | 错误率计算的最小请求数 |

### 7.3 重建执行配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `rebuild_check_interval` | time.Duration | 5m | 重建检查间隔 |
| `rebuild_batch_size` | int | 5 | 批量重建大小 |
| `rebuild_concurrency` | int | 3 | 并发重建数 |

### 7.4 配置示例

```yaml
# 健康检查配置
health_check_time: 30s
health_check_timeout: 5s
degraded_failure_threshold: 1      # 首次失败进入降级状态
unhealthy_failure_threshold: 3     # 连续失败3次进入不健康状态
health_check_trigger_rebuild: true
rebuild_on_degraded: false         # 降级时不重建（可选）

# 智能重建配置
smart_rebuild_enabled: true
rebuild_strategy: "any"            # any|all|usage|age|error
rebuild_max_usage_count: 200
rebuild_max_age: 30m
rebuild_max_error_rate: 0.2
rebuild_min_interval: 5m
rebuild_min_requests_for_error_rate: 10

# 重建执行配置
rebuild_check_interval: 5m
rebuild_batch_size: 5
rebuild_concurrency: 3
```

## 8. 最佳实践

### 8.1 使用上下文控制超时

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

result, err := pool.RebuildConnectionByID(ctx, connID)
if errors.Is(err, context.DeadlineExceeded) {
    log.Errorf("重建超时")
}
```

### 8.2 批量重建后检查失败连接

```go
result, err := pool.RebuildConnectionByProto(ctx, "ssh")
if err != nil {
    return err
}

for _, r := range result.Results {
    if !r.Success {
        log.Errorf("连接 %s 重建失败: %s", r.OldConnID, r.Error)
    }
}
```

## 9. 设计原则

### 9.1 状态分离原则
- **连接状态** (`ConnectionState`)：操作生命周期（**怎么做**）
- **健康状态** (`HealthStatus`)：质量监控（**怎么样**）
- **重建标记** (`markedForRebuild`)：恢复决策（**何时恢复**）

### 9.2 并发控制设计
- **决策阶段并发控制**：`markedForRebuild` 原子标记
- **执行阶段并发控制**：`isRebuilding` 管理标记 + 连接锁
- **两阶段协同工作**：防止并发重复决策和并发执行

### 9.3 简洁API原则
- 提供清晰的公开API，避免冗余
- 同步API用于手动操作
- 异步API用于HTTP请求场景
- 批量操作使用Worker Pool并发控制

---

*文档版本: 2.0*  
*最后更新: 2025-12-30*  
*文档类型: 设计文档*
