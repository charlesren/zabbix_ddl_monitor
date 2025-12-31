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

## 10. 完整调用链路

### 10.1 健康检查调用链

```
healthCheckTask() (每30秒触发)
  ↓
performHealthChecks()
  ↓
performHealthCheckForProtocol(proto)
  ↓
GetWithContext(ctx, proto) → 从连接池获取连接
  ↓
【连接获取】
  ├─ tryGetAvailableConnection() → 尝试获取可用连接
  │   └─ conn.tryAcquireForTask() → StateIdle → StateAcquired
  │       └─ 检查: state==StateIdle && health!=HealthStatusUnhealthy && rebuilding==false
  │       └─ 只返回空闲、非不健康、非重建中的连接
  │
  ├─ 如果没有可用连接 → tryCreateNewConnection()
  │   ├─ 检查当前连接数 < maxConnections
  │   └─ 创建新连接并获取
  │
  └─ 返回 MonitoredDriver 包装的连接
  ↓
【执行健康检查】
  ├─ conn.beginHealthCheck() → StateAcquired → StateChecking
  ├─ checkConnectionHealthWithContext(ctx, conn)
  │   ├─ 跳过15秒内的新连接
  │   ├─ defaultHealthCheck() → 执行 "show clock" 或 GetPrompt
  │   └─ 超时控制: health_check_timeout (默认5秒)
  │
  └─ conn.recordHealthCheck(success, err)
      ├─ 成功: consecutiveFailures=0 → HealthStatusHealthy
      ├─ 失败: consecutiveFailures++
      │   ├─ ≥ DegradedFailureThreshold(1) → HealthStatusDegraded
      │   └─ ≥ UnhealthyFailureThreshold(3) → HealthStatusUnhealthy
      │       └─ 如果 HealthCheckTriggerRebuild=true
      │           └─ markForRebuildWithReasonLocked()
      │               └─ markedForRebuild=1 (原子操作)
      │
      └─ StateChecking → StateAcquired
  ↓
【释放连接】
  ├─ Release(driver) → conn.release()
  └─ StateAcquired → StateIdle
```

**关键点说明**：

1. **连接过滤**: `tryAcquireForTask()` 只返回满足条件的连接：
   - `state == StateIdle`（空闲状态）
   - `health != HealthStatusUnhealthy`（非不健康状态）
   - `rebuilding == false`（不在重建中）
   - `useCount == 0`（没有被其他任务使用）
   
   **重要**：正在重建中的连接（`rebuilding=true`）不会被获取用于执行任务。

2. **连接创建**: 如果没有可用连接且未达到 `maxConnections`，会自动创建新连接
   - 这确保了即使重建失败导致连接数减少，也能自动补充

3. **健康检查间隔**: 默认30秒检查一次，每次检查每个协议的一个连接

4. **年龄限制**: 15秒内的新连接跳过健康检查，避免连接尚未稳定

### 10.2 连接重建调用链

```
rebuildTask() (每5分钟触发，可配置 RebuildCheckInterval)
  ↓
performRebuilds()
  ↓
【第一阶段：评估并标记所有连接】
RebuildConnectionByProto(ctx, proto)
  ↓
【智能评估】如果 SmartRebuildEnabled=true
  ├─ getAllConnectionsByProto(proto) → 获取该协议所有连接
  └─ 对每个连接调用:
      └─ RebuildManager.EvaluateAndMark(conn)
          ├─ 快速路径: 检查是否已标记 (markedForRebuild==1)
          ├─ 检查重建是否启用
          ├─ 检查重建间隔 (距上次创建/重建 ≥ RebuildMinInterval)
          ├─ 检查连接状态 (必须是 StateIdle)
          └─ evaluateByStrategyAndMark()
              ├─ 根据策略评估 (usage/age/error/any/all)
              └─ 如果满足条件:
                  └─ markForRebuildWithReason(reason)
                      └─ markedForRebuild=0 → 1 (原子操作)
  ↓
【第二阶段：批量执行重建】
getConnectionsForRebuild(proto) → 获取已标记的连接 (markedForRebuild==1)
  ↓
【Worker Pool 并发执行】
  ├─ 并发度: RebuildConcurrency (默认3)
  ├─ 批次大小: RebuildBatchSize (默认5)
  └─ 对每个需要重建的连接:
      └─ rebuildConnection(ctx, conn)
          └─ performCoreRebuild(ctx, conn)
              │
              ├─【阶段1：快速检查】(无锁)
              │   ├─ canStartRebuild(conn)
              │   │   ├─ conn.isIdle() → state==StateIdle
              │   │   └─ conn.getUseCount()==0
              │   └─ 返回 false 则跳过重建
              │
              ├─【阶段2：开始重建】(连接锁)
              │   └─ conn.beginRebuild()
              │       ├─ 检查 !conn.isRebuilding()
              │       └─ conn.rebuilding=true (设置执行标记)
              │
              ├─【阶段3：关闭旧连接】
              │   ├─ conn.beginClose() → StateIdle → StateClosing
              │   ├─ conn.driver.Close() → 关闭底层连接
              │   ├─ 从连接池删除 (池锁)
              │   │   └─ delete(pool.connections, oldID)
              │   └─ conn.completeClose() → StateClosing → StateClosed
              │
              ├─【阶段4：创建新连接】(无锁，5-10秒)
              │   ├─ createReplacementConnection(ctx, pool, oldConn)
              │   │   ├─ createConnection(ctx, pool)
              │   │   │   ├─ factory.CreateWithContext(ctx, config)
              │   │   │   └─ 创建新连接对象
              │   │   └─ newConn.setLastRebuiltAt(now)
              │   │
              │   └─ ⚠️ 如果失败:
              │       └─ 返回错误，旧连接已从池删除
              │           └─ 连接池会通过 GetWithContext() 自动补充
              │
              ├─【阶段5：添加新连接】(池锁)
              │   └─ pool.connections[newConn.id] = newConn
              │
              └─【阶段6：完成重建】(无锁)
                  ├─ completeRebuild(success)
                  │   ├─ conn.rebuilding=false
                  │   └─ 如果成功: markedForRebuild=0
                  │       └─ 如果失败: 保持 markedForRebuild=1 (下次重试)
                  └─ sendEvent(EventConnectionRebuilt)
```

**关键点说明**：

1. **全量评估**: 定期重建任务会评估**所有连接**（不只是被健康检查标记的）
   - 评估条件: 使用次数、连接年龄、错误率（根据策略）
   - 每个连接都有总执行次数和总时间阈值

2. **决策与执行分离**:
   - 决策阶段: `EvaluateAndMark()` → `markedForRebuild=1`
   - 执行阶段: `rebuildTask()` → 检测标记并执行重建

3. **并发控制**:
   - `markedForRebuild` (原子操作): 防止并发重复决策
   - `rebuilding` (连接锁): 防止并发执行重建

4. **失败重试**: 重建失败后保持 `markedForRebuild=1`，下次重建任务会重试

### 10.3 任务执行调用链（对比）

```
【任务执行】(如 PingTask)
manager → RouterScheduler → IntervalTaskQueue
  ↓
asyncExecutor.Submit(task)
  ↓
processTask()
  ↓
executor.Execute(task)
  ↓
【获取连接】pool.GetWithContext(ctx, proto)
  ├─ tryGetAvailableConnection()
  │   └─ 只返回 state==StateIdle && health!=HealthStatusUnhealthy && rebuilding==false
  └─ tryCreateNewConnection()
      ├─ 检查 currentCount < maxConnections
      └─ 创建新连接
  ↓
【执行任务】task.Execute(driver)
  ↓
【释放连接】pool.Release(driver)
  └─ conn.release() → StateAcquired → StateIdle
```

**与健康检查的区别**：
- 健康检查: 有意获取连接进行检查，然后释放
- 任务执行: 正常业务流程，获取连接执行任务
- 两者都会调用 `GetWithContext()`，遵循相同的连接获取规则

## 11. 关键设计说明（消除常见误解）

### 11.1 问题1: 健康检查和重建的时序竞态 ✅ 设计内

**场景**: 健康检查失败并标记需要重建，重建任务开始执行时，连接可能被其他任务使用。

**设计保证**:

1. **连接获取过滤**:
   ```go
   // tryAcquireForTask() 只返回满足条件的连接
   if conn.state != StateIdle {
       return false
   }
   if conn.healthStatus == HealthStatusUnhealthy {
       return false  // 不健康的连接不会被获取
   }
   if conn.rebuilding {
       return false  // 重建中的连接不会被获取
   }
   ```

2. **重建前置检查**:
   ```go
   // canStartRebuild() 确保连接空闲
   if !conn.isIdle() {  // state == StateIdle
       return false
   }
   if conn.getUseCount() > 0 {  // usingCount == 0
       return false
   }
   ```

3. **状态保护**:
   - `beginRebuild()` 检查连接不在执行中
   - `rebuilding=true` 防止并发操作
   - `tryAcquireForTask()` 检查 `rebuilding==false` 防止获取重建中的连接

**结论**: 不存在竞态问题，多层检查确保安全。

### 11.2 问题2: 健康检查期间连接不可用 ✅ 设计内

**场景**: 健康检查持有连接期间，其他任务无法使用该连接。

**设计考虑**:

1. **健康检查是轻量级操作**:
   - 执行简单命令（如 `show clock`）
   - 超时控制: 默认5秒
   - 实际通常 < 1秒

2. **连接池设计**:
   - 每个协议通常有多个连接（`minConnections`）
   - 健康检查只占用一个连接
   - 不影响整体可用性

3. **健康检查间隔**: 30秒一次，低频操作

**结论**: 影响可控，符合连接池设计预期。

### 11.3 问题3: 重建期间连接数短暂减少 ✅ 设计内

**场景**: 在创建新连接期间（5-10秒），连接池中该协议的连接数会减少1。

**设计考虑**:

1. **时间窗口短**:
   - 重建是低频操作（5分钟一次）
   - 创建新连接通常 < 10秒

2. **连接池冗余**:
   - 通常有多个连接（`minConnections` ≥ 2）
   - 短暂减少一个连接不影响可用性

3. **自动补充机制**:
   ```go
   // GetWithContext() 中
   if 没有空闲连接 && currentCount < maxConnections {
       tryCreateNewConnection()  // 自动创建新连接
   }
   ```

**结论**: 影响很小，设计内考虑。

### 11.4 问题4: 重建失败后的连接恢复 ✅ 有自动恢复

**场景**: 重建失败后，旧连接已删除，新连接未创建，连接池连接数减少。

**详细分析**:

**重建失败后的状态**:
```
performCoreRebuild() 失败
  ↓
阶段3: 旧连接已从池删除并关闭
  - delete(pool.connections, oldID)  ← 已删除
  - oldConn.driver.Close()           ← 已关闭
  - oldConn.state = StateClosed      ← 已关闭
  ↓
阶段4: 创建新连接失败
  - 返回错误
  ↓
defer: completeRebuild(false)
  - conn.rebuilding = false
  - 保持 markedForRebuild = 1  ← 标记保留，下次重试
```

**自动恢复机制**:

1. **下次重建任务重试**:
   ```
   5分钟后 → rebuildTask() 再次执行
     ↓
   EvaluateAndMark() 检测到 markedForRebuild==1
     ↓
   跳过评估（已标记），直接进入重建流程
   ```

2. **GetWithContext() 自动补充**:
   ```go
   // 如果没有空闲连接且未达到 maxConnections
   conn, err = p.tryCreateNewConnection(ctx, pool)
   if conn != nil {
       return conn, nil  // 创建成功，补充连接
   }
   ```

3. **重试策略**:
   - 连接创建使用 `ResilientExecutor`（带重试）
   - 最大重试次数: `ConnectionMaxRetries`（可配置）
   - 指数退避: `ConnectionRetryInterval * BackoffFactor`

**重建失败不会导致连接永久丢失**:

| 情况 | 行为 | 结果 |
|------|------|------|
| 重建失败 | 旧连接已删除，新连接未创建 | 连接数-1 |
| 下次任务执行 | `GetWithContext()` 无空闲连接 | 自动创建新连接（如果未满） |
| 下次重建任务 | 检测到 `markedForRebuild==1` | 重试重建 |

**结论**: 重建失败有自动恢复机制，不会导致连接永久减少。

### 11.5 问题5: 健康检查失败阈值固定 ✅ 可配置

**配置参数**:
```yaml
degraded_failure_threshold: 1   # 可配置
unhealthy_failure_threshold: 3  # 可配置
```

**灵活性**:
- 可根据网络稳定性调整
- 关键业务可设置更严格的阈值
- 非关键业务可设置更宽松的阈值

### 11.6 问题6: 健康检查只检查一个连接 ✅ 设计内 + 重建评估补充

**健康检查**: 每次检查每个协议的一个连接（轮询）
- 目的: 低开销监控
- 频率: 30秒一次

**重建评估**: 定期评估**所有连接**
- 频率: 5分钟一次
- 范围: 该协议的所有连接
- 条件: 使用次数、年龄、错误率

**协同工作**:
```
健康检查 → 快速发现问题 → 标记重建
重建评估 → 全面评估 → 标记重建
重建任务 → 执行所有标记的重建
```

**结论**: 健康检查 + 重建评估 = 全覆盖。

### 11.7 问题7: 状态转换的限制 ✅ 设计内 + 依赖重建评估

**场景**: 某些连接可能长期处于不健康状态但未及时重建。

**多层保护**:

1. **健康检查触发**:
   ```go
   if consecutiveFailures >= unhealthyThreshold {
       markForRebuildWithReasonLocked()  // 立即标记
   }
   ```

2. **定期重建评估** (5分钟):
   ```go
   // 评估所有连接，不只检查标记的
   EvaluateAndMark() {
       // 检查所有条件: usage, age, error
       // 即使健康检查未触发，也会因其他条件标记
   }
   ```

3. **清理任务** (1分钟):
   ```go
   // 清理不健康的连接
   shouldCleanupConnection() {
       if health == HealthStatusUnhealthy {
           return true  // 标记清理
       }
   }
   ```

**结论**: 多层机制确保问题连接会被处理。

## 12. 重建失败后的状态管理详解

### 12.1 重建失败的六阶段状态变化

```
【初始状态】
conn.state = StateIdle
conn.health = HealthStatusUnhealthy (可能)
conn.markedForRebuild = 1
conn.rebuilding = false

【阶段1：快速检查】
→ 检查通过: conn.isIdle() && conn.getUseCount()==0

【阶段2：开始重建】
→ conn.rebuilding = true
→ conn.markedForRebuild = 1 (保持)

【阶段3：关闭旧连接】
→ conn.beginClose()
→ conn.state: Idle → Closing
→ conn.driver.Close()
→ delete(pool.connections, conn.id)  ← 从池删除
→ conn.completeClose()
→ conn.state: Closing → Closed

【阶段4：创建新连接】
→ createReplacementConnection(ctx, pool, conn)
→ 如果失败: 返回错误
→ 此时: 旧连接已从池删除且关闭，新连接未创建

【阶段5：跳过】（因为失败）
→ 未执行添加新连接

【阶段6：完成重建】
→ defer 触发: completeRebuild(false)
→ conn.rebuilding = false
→ conn.markedForRebuild = 1 (保持，用于下次重试)
→ conn.state = StateClosed (终态)

【最终状态】
旧连接 (conn):
  - state = StateClosed (终态，不可再用)
  - rebuilding = false
  - markedForRebuild = 1 (保留标记)
  - 已从 pool.connections 删除
  - driver 已关闭

连接池:
  - 连接数 -1 (旧连接已删除)
  - 没有新连接加入
```

### 12.2 重建失败后的自动恢复机制

#### 机制1: 下次重建任务重试（5分钟后）

```
rebuildTask() (5分钟后触发)
  ↓
RebuildConnectionByProto(ctx, proto)
  ↓
getAllConnectionsByProto(proto)
  ↓
⚠️ 注意: 旧连接已从池删除，不在连接列表中
```

**关键点**: 
- 旧连接已从池删除，不会再被评估

#### 机制2: GetWithContext() 自动创建新连接

```
【任务执行】
task.Execute()
  ↓
pool.GetWithContext(ctx, proto)
  ↓
tryGetIdleConnection()
  → 返回 nil (没有空闲连接)
  ↓
tryCreateNewConnection()
  → 检查 currentCount < maxConnections
  → createConnection(ctx, pool)
  → 添加新连接到池
  → 返回新连接
```

**触发条件**:
1. 没有空闲连接
2. 当前连接数 < maxConnections
3. 任务需要执行

**结果**: 连接池自动补充连接数。

#### 机制3: 连接创建重试（ResilientExecutor）

```
createConnection(ctx, pool)
  ↓
factory.CreateWithContext(ctx, config)
  ↓
使用 ResilientExecutor:
  ├─ 最大重试: ConnectionMaxRetries (可配置)
  ├─ 指数退避: ConnectionRetryInterval * BackoffFactor^attempt
  └─ 超时控制: ConnectTimeout * (MaxRetries + 1)
```

**即使首次创建失败，也会自动重试**。

### 12.3 重建失败的影响范围

| 影响项 | 状态 | 恢复机制 |
|--------|------|----------|
| 连接池连接数 | -1 (暂时) | `GetWithContext()` 自动创建 |
| 旧连接对象 | StateClosed | 不再使用，等待GC |
| 重建标记 | 保持 markedForRebuild=1 | 不再有效（连接已删除） |
| 任务执行 | 正常 | 自动创建新连接 |
| 健康检查 | 正常 | 检查其他连接 |

**不会影响**:
- ❌ 任务执行（有自动创建机制）
- ❌ 其他连接的重建
- ❌ 健康检查流程
- ❌ 连接池整体可用性

**会影响的场景**:
- ⚠️ 如果 `maxConnections` 已满，无法自动创建
- ⚠️ 如果重建原因持续存在（如目标设备不可达），新连接也会失败

### 12.4 重建失败后的典型时间线

```
T+0s:    重建失败，旧连接删除
T+0s:    任务执行，GetWithContext() 无空闲连接
T+0s:    自动创建新连接（如果未满）
T+0s:    新连接创建成功，任务继续执行
         或
T+0s:    新连接创建失败（maxConnections已满或设备不可达）
T+0s:    任务执行失败，返回错误

T+0s~T+5min:  连接数可能减少（取决于任务触发频率）

T+5min:   下次重建任务触发
T+5min:   评估所有连接，标记需要重建的连接
T+5min:   执行重建（使用新的创建上下文）
T+5min:   如果设备恢复，重建成功，连接数恢复
```

### 12.5 特殊场景分析

#### 场景1: maxConnections已满

```
连接池已满: 10/10
重建失败: 删除1个 → 9/10
任务执行: GetWithContext() 无空闲连接
          tryCreateNewConnection() 检查 9 < 10 ✅
          创建新连接 → 10/10 ✅
```

**结果**: 自动恢复到满额。

#### 场景2: 目标设备不可达

```
设备不可达
  ↓
所有健康检查失败
  ↓
所有连接标记为 Unhealthy
  ↓
重建任务尝试重建
  ↓
所有连接重建失败（设备不可达）
  ↓
连接池最终为空
  ↓
任务执行失败，返回错误
```

**这是预期行为**: 设备不可达时，无法创建新连接。

**恢复**: 设备恢复后，下次任务执行时会自动创建新连接。

#### 场景3: 间歇性网络问题

```
T+0s:    重建失败（网络临时不可达）
T+1s:    任务执行，自动创建新连接成功
T+5min:  下次重建任务，正常重建其他连接
```

**结果**: 快速恢复，影响很小。

### 12.6 结论

1. **重建失败不会导致连接永久丢失**:
   - `GetWithContext()` 有自动创建机制
   - 连接创建有重试机制
   - 下次重建任务会处理其他连接

2. **连接池具备自愈能力**:
   - 重建失败 → 连接数减少
   - 任务执行 → 自动创建新连接
   - 连接数恢复

3. **唯一的不可恢复场景**:
   - `maxConnections` 已满 且 设备持续不可达
   - 这是系统资源限制和外部依赖的合理约束

4. **设计冗余**:
   - 定期重建任务 (5分钟)
   - 健康检查 (30秒)
   - 清理任务 (1分钟)
   - 多层机制确保问题会被发现和处理

## 13. 连接清理机制

### 13.1 清理任务概述

清理任务是连接池维护的核心机制，负责定期清理不再适合使用的连接。与重建任务不同，清理任务**只负责删除连接，不创建新连接**。

**执行频率**: 每1分钟一次（可配置）

**核心职责**:
1. 清理不健康的连接
2. 清理僵尸连接（状态超时）
3. 清理空闲超时的连接

### 13.2 清理与重建的职责划分

| 职责 | 清理任务 | 重建任务 |
|------|---------|---------|
| 不健康连接 | ✅ 删除 | ✅ 删除并创建 |
| 僵尸连接（超时） | ✅ 删除 | ✅ 删除并创建 |
| 空闲超时 | ✅ 仅删除 | ❌ 不处理 |
| 生命周期超时 | ❌ 不处理 | ✅ 删除并创建 |
| 使用次数超限 | ❌ 不处理 | ✅ 删除并创建 |
| 策略条件 | ❌ 不处理 | ✅ 删除并创建 |

**设计原则**:
- 清理任务：快速移除问题连接，防止占用连接池位置
- 重建任务：周期性评估，有计划地替换老化连接

### 13.3 清理调用链

```
cleanupTask() (每1分钟触发)
  ↓
cleanupConnections()
  ↓
对每个协议的连接池:
  ↓
for connID, conn := range pool.connections {
    ↓
    【第一步：判断是否需要清理】
    if shouldCleanupConnection(conn, now) {
        ↓
        【第二步：尝试获取清理权限】
        if conn.tryAcquireForCleanup() {
            ↓
            【第三步：从连接池删除】
            delete(pool.connections, connID)
            
            【第四步：释放使用计数】
            conn.release()  // 对应 tryAcquireForCleanup 的 acquire
            
            【第五步：异步安全关闭】
            go func(c *EnhancedPooledConnection) {
                c.safeClose()  // 调用 acquireUse/releaseUse 配对
            }(conn)
        }
    }
}
```

**关键点说明**:

1. **tryAcquireForCleanup() vs tryAcquireForTask()**:
   ```go
   // tryAcquireForTask() - 用于任务执行
   // 检查: state == StateIdle && health != Unhealthy && rebuilding == false
   
   // tryAcquireForCleanup() - 用于清理任务
   // 检查: state == StateIdle && useCount == 0
   // 不检查 health 和 rebuilding（由 shouldCleanupConnection 处理）
   ```

2. **shouldCleanupConnection() 分层判断**:
   - 优先级1: 不健康连接（立即清理）
   - 优先级2: 僵尸连接（非Idle状态超时）
   - 优先级3: Idle+rebuilding 异常
   - 优先级4: 空闲超时

3. **usageCount 配对管理**:
   ```
   tryAcquireForCleanup(): usingCount 0→1
   conn.release():          usingCount 1→0
   safeClose(): acquireUse 0→1, releaseUse 1→0
   
   最终: usingCount = 0 ✅ 正确配对
   ```

### 13.4 shouldCleanupConnection() 详细逻辑

#### 优先级1: 不健康连接（立即清理）

```go
health := conn.getHealthStatus()
if health == HealthStatusUnhealthy {
    ylog.Infof("connection_pool", "清理不健康连接: id=%s", conn.id)
    return true  // 即使 rebuilding=true，也强制清理
}
```

**特点**:
- 最高优先级
- 不检查 `rebuilding` 标志
- 立即清理，防止不健康连接被使用

#### 优先级2: 僵尸连接（状态超时检测）

```go
state := conn.getState()
lastUsed := conn.getLastUsedTime()
idleTime := now.Sub(lastUsed)

var timeout time.Duration
var reason string

switch state {
case StateClosing:
    timeout = p.config.StuckTimeoutClosing
    if timeout == 0 {
        timeout = 1 * time.Minute // 默认1分钟
    }
    reason = "关闭超时"

case StateConnecting:
    timeout = p.config.StuckTimeoutConnecting
    if timeout == 0 {
        timeout = 30 * time.Second // 默认30秒
    }
    reason = "连接超时"

case StateAcquired:
    timeout = p.config.StuckTimeoutAcquired
    if timeout == 0 {
        timeout = 5 * time.Minute // 默认5分钟
    }
    reason = "获取超时"

case StateExecuting:
    timeout = p.config.StuckTimeoutExecuting
    if timeout == 0 {
        timeout = 5 * time.Minute // 默认5分钟
    }
    reason = "执行超时"

case StateChecking:
    timeout = p.config.StuckTimeoutChecking
    if timeout == 0 {
        timeout = 2 * time.Minute // 默认2分钟
    }
    reason = "检查超时"

case StateClosed:
    // 终态，立即清理
    ylog.Infof("connection_pool", "清理已关闭连接: id=%s", conn.id)
    return true

default:
    timeout = 5 * time.Minute
    reason = "状态异常"
}

// 超时检测：即使 rebuilding=true，超时也强制清理
if idleTime > timeout {
    ylog.Warnf("connection_pool",
        "清理%s连接: id=%s, state=%s, rebuilding=%v, idle=%v",
        reason, conn.id, state, conn.rebuilding, idleTime)
    return true // 强制清理，中断可能的重建
}
```

**超时配置**:

| 配置参数 | 默认值 | 说明 |
|---------|--------|------|
| `StuckTimeoutClosing` | 1分钟 | Closing状态超时（关闭操作需要时间） |
| `StuckTimeoutConnecting` | 30秒 | Connecting状态超时（连接建立） |
| `StuckTimeoutAcquired` | 5分钟 | Acquired状态超时（获取后未执行） |
| `StuckTimeoutExecuting` | 5分钟 | Executing状态超时（执行时间过长） |
| `StuckTimeoutChecking` | 2分钟 | Checking状态超时（健康检查） |

**设计考虑**:
- 不同状态有不同的合理超时时间
- Closing需要较长时间（可能有关闭握手）
- Connecting应该快速完成（30秒）
- Executing可能需要较长时间（复杂命令）
- 即使 `rebuilding=true`，超时也强制清理（清理优先级 > 重建优先级）

#### 优先级3: Idle+rebuilding 异常

```go
if state == StateIdle && conn.rebuilding {
    rebuildWaitTime := now.Sub(lastUsed)
    timeout := p.config.StuckTimeoutRebuildIdle
    if timeout == 0 {
        timeout = 10 * time.Second // 默认10秒
    }
    if rebuildWaitTime > timeout {
        ylog.Warnf("connection_pool",
            "清理重建异常的Idle连接: id=%s, rebuilding=%v, wait=%v",
            conn.id, conn.rebuilding, rebuildWaitTime)
        return true
    }
    // 正在重建，等待状态转换
    return false
}
```

**场景**: 连接处于 `StateIdle` 但标记为 `rebuilding=true`，且长时间未转换状态

**可能原因**:
1. 重建过程中断（连接意外回到Idle状态）
2. 状态转换逻辑错误
3. 并发竞态条件

**超时配置**:
- `StuckTimeoutRebuildIdle`: 默认10秒

#### 优先级4: 空闲超时（只清理，不创建）

```go
if state == StateIdle {
    // 只检查空闲超时（清理任务只删除，不创建）
    // 生命周期超时、使用次数超限由重建任务负责（会创建新连接）
    if now.Sub(lastUsed) > p.idleTimeout {
        ylog.Infof("connection_pool",
            "清理空闲超时连接: id=%s, idle=%v, timeout=%v",
            conn.id, now.Sub(lastUsed), p.idleTimeout)
        return true  // 只删除，不创建新连接
    }
}
```

**关键点**:
- **只删除，不创建** - 与重建任务的区别
- 空闲超时由 `p.idleTimeout` 配置控制
- 生命周期超时、使用次数由重建任务处理

### 13.5 清理优先级原则

**清理任务优先级 > 重建任务优先级**

体现为：
1. 不健康连接即使 `rebuilding=true` 也强制清理
2. 僵尸连接即使 `rebuilding=true` 超时也强制清理
3. 防止重建过程卡住导致连接池位置永久占用

**为什么这样设计**:
- 重建可能因为网络问题、设备问题长时间卡住
- 清理任务是兜底机制，确保问题连接最终会被移除
- 连接池会通过 `GetWithContext()` 自动创建新连接来补充

### 13.6 清理任务配置参数

**新增配置参数**:

```yaml
# 僵尸连接超时配置（用于清理任务的状态超时检测）
stuck_timeout_closing: 1m           # Closing状态超时，默认1分钟
stuck_timeout_connecting: 30s       # Connecting状态超时，默认30秒
stuck_timeout_acquired: 5m          # Acquired状态超时，默认5分钟
stuck_timeout_executing: 5m         # Executing状态超时，默认5分钟
stuck_timeout_checking: 2m          # Checking状态超时，默认2分钟
stuck_timeout_rebuild_idle: 10s     # Idle+rebuilding状态超时，默认10秒
```

### 13.7 清理任务与重建任务的协调

#### 场景1: 重建任务正在执行，清理任务触发

```
【重建任务】
conn.rebuilding = true
conn.state: Idle → Closing → Closed
[创建新连接中...]

【清理任务触发】
shouldCleanupConnection(conn):
  ├─ 检查 health != Unhealthy ✅
  ├─ 检查 state == Closed → 立即清理 ✅
  └─ 返回 true

【结果】
  → tryAcquireForCleanup() 检查 state != StateIdle
  → 返回 false（不会清理）
```

**保护机制**:
- `tryAcquireForCleanup()` 只接受 `StateIdle` 的连接
- 非Idle状态由超时检测处理
- 重建任务会快速完成状态转换（Closing < 1分钟）

#### 场景2: 重建卡住，清理任务超时强制清理

```
【重建任务卡住】
conn.rebuilding = true
conn.state = StateClosing
连接关闭操作卡住（网络问题、设备无响应）

【清理任务触发】
shouldCleanupConnection(conn):
  ├─ 检查 state = StateClosing
  ├─ 检查 idleTime > 1分钟
  └─ 返回 true（强制清理）

【结果】
  → tryAcquireForCleanup() 检查 state != StateIdle
  → 返回 false（不会清理）

⚠️ 问题: Closing状态的连接无法被 tryAcquireForCleanup 清理
```

**改进方案**:

修改 `tryAcquireForCleanup()` 以支持清理非Idle状态：

```go
func (conn *EnhancedPooledConnection) tryAcquireForCleanup() bool {
    // 清理任务可以获取任何状态的连接（除了已被其他清理操作获取的）
    // 因为 shouldCleanupConnection 已经做了充分的判断
    
    if !atomic.CompareAndSwapInt64(&conn.usingCount, 0, 1) {
        return false
    }
    
    // 清理不需要状态转换，直接返回
    return true
}
```

**当前实现限制**:
- `tryAcquireForCleanup()` 只接受 `StateIdle`
- 非Idle僵尸连接需要等待状态自然转换或超时

**为什么当前可以这样设计**:
- 大部分僵尸连接最终会转换到 `StateClosed`（终态）
- `StateClosed` 会被超时检测立即清理
- `StateIdle` + `rebuilding` 异常会被10秒超时清理
- 实际场景中，卡在非Idle状态较少见

#### 场景3: 清理任务删除连接，重建任务尝试访问

```
【清理任务】
delete(pool.connections, connID)
conn.safeClose()

【重建任务】
performCoreRebuild():
  ├─ 检查 markedForRebuild == 1 ✅
  ├─ canStartRebuild(conn)
  │   ├─ conn.isIdle() → true
  │   └─ conn.getUseCount() → 可能 > 0（清理任务持有）
  └─ 返回 false（跳过重建）
```

**保护机制**:
- `canStartRebuild()` 检查 `useCount == 0`
- 清理任务持有期间，重建任务会跳过
- 清理完成后，连接已从池删除，不会再被评估

### 13.8 清理任务的兜底作用

清理任务在整个连接池维护中的定位：

```
【第一层：健康检查】（30秒）
  ↓
  快速发现问题 → 标记重建

【第二层：重建任务】（5分钟）
  ↓
  周期性评估所有连接 → 标记并执行重建
  处理: 使用次数、年龄、错误率

【第三层：清理任务】（1分钟）← 兜底机制
  ↓
  快速清理问题连接
  处理: 不健康、僵尸、空闲超时
  特点: 只删除，不创建（由GetWithContext自动创建补充）
```

**为什么需要清理任务**:

1. **快速响应**: 1分钟执行一次，比重建任务（5分钟）更频繁
2. **防止污染**: 快速移除不健康连接，防止任务获取到
3. **兜底保护**: 捕获状态转换异常、僵尸连接等边界情况
4. **资源释放**: 清理空闲连接，释放连接池位置

**与重建任务的协同**:
- 清理任务: "快速移除问题"
- 重建任务: "有计划地替换"
- 两者配合，确保连接池健康和高可用

### 13.9 清理任务最佳实践

#### 配置建议

```yaml
# 僵尸连接超时配置
stuck_timeout_closing: 1m           # 关闭操作通常1分钟内完成
stuck_timeout_connecting: 30s       # 连接建立应该快速完成
stuck_timeout_acquired: 3m          # 根据实际任务执行时间调整
stuck_timeout_executing: 5m         # 根据最慢命令调整
stuck_timeout_checking: 2m          # 健康检查通常< 5秒，2分钟已宽松
stuck_timeout_rebuild_idle: 10s     # 重建应该快速转换状态

# 空闲超时（由连接池配置）
max_idle_time: 10m                  # 空闲10分钟清理（只删不建）
```

#### 监控指标

建议监控以下指标：
- 每分钟清理的连接数（按原因分类）
- 清理后的连接池大小
- 清理任务的执行耗时
- 僵尸连接出现的频率

#### 告警规则

```
# 1分钟内清理连接数 > 10
alert: HighConnectionCleanupRate
expr: rate(connections_cleaned_total[1m]) > 10
severity: warning

# 僵尸连接（超时清理）频率过高
alert: HighStuckConnectionRate
expr: rate(connections_cleaned_by_timeout[1m]) > 5
severity: warning
```

---

*文档版本: 4.0*  
*最后更新: 2025-12-31*  
*文档类型: 设计文档*
