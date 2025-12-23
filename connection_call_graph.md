# 连接池函数调用关系图

## 概述
本文档记录`EnhancedConnectionPool`模块的函数调用关系，用于指导重构工作。

## 主要调用路径

### 1. 连接获取主路径（高频调用）

```
GetWithContext(ctx, proto)
    ├── getDriverPool(proto)                    # 获取驱动池
    ├── resilientExecutor.Execute()             # 弹性执行器
    │   └── getConnectionFromPool(ctx, pool)    # 从池中获取连接
    │       ├── tryGetIdleConnection(ctx, pool) # 尝试获取空闲连接
    │       │   ├── pool.mu.RLock()             # 池读锁（收集连接ID）
    │       │   ├── pool.mu.RUnlock()
    │       │   ├── conn.tryAcquire()           # 尝试获取连接
    │       │   │   ├── conn.mu.Lock()
    │       │   │   ├── transitionStateLocked(StateAcquired)
    │       │   │   └── conn.mu.Unlock()
    │       │   ├── conn.isHealthy()            # 检查健康状态
    │       │   ├── shouldRebuildConnection(conn) # 检查是否需要重建
    │       │   │   ├── conn.getStatus()
    │       │   │   ├── conn.isInUse()
    │       │   │   ├── conn.isHealthy()
    │       │   │   ├── conn.isMarkedForRebuild()
    │       │   │   └── 根据策略判断
    │       │   ├── markConnectionForRebuildAsync() # 标记重建
    │       │   │   └── asyncRebuildConnection()
    │       │   └── pool.mu.Lock()              # 池写锁（更新统计）
    │       │       └── pool.mu.Unlock()
    │       └── tryCreateNewConnection(ctx, pool) # 尝试创建新连接
    │           ├── pool.mu.RLock()             # 池读锁（检查连接数）
    │           ├── pool.mu.RUnlock()
    │           ├── createConnection(ctx, pool) # 创建新连接
    │           │   ├── pool.factory.CreateWithContext()
    │           │   └── 创建EnhancedPooledConnection
    │           ├── pool.mu.Lock()              # 池写锁（添加连接）
    │           ├── newConn.tryAcquire()
    │           └── pool.mu.Unlock()
    └── activateConnection(conn)                # 激活连接
        ├── getDriverPool(conn.protocol)
        ├── 更新池统计
        └── 创建MonitoredDriver
```

### 2. 连接释放路径

```
Release(driver)
    ├── 类型检查（MonitoredDriver）
    ├── getDriverPool(protocol)
    ├── pool.mu.Lock()
    ├── conn.release()
    │   ├── conn.mu.Lock()
    │   ├── transitionStateLocked(StateIdle)
    │   └── conn.mu.Unlock()
    ├── 更新池统计
    ├── 更新性能指标
    └── pool.mu.Unlock()
```

### 3. 健康检查路径（定时任务）

```
healthCheckTask()
    └── performHealthChecks()
        ├── 遍历所有协议池
        ├── pool.mu.RLock()                    # 池读锁（收集空闲连接）
        ├── pool.mu.RUnlock()
        └── checkConnectionHealth(proto, conn) # 检查单个连接健康
            ├── conn.isInUse()                 # 跳过正在使用的连接
            ├── conn.beginHealthCheck()        # 开始健康检查
            │   ├── conn.mu.Lock()
            │   ├── transitionStateLocked(StateChecking)
            │   └── conn.mu.Unlock()
            ├── defaultHealthCheck(driver)     # 执行健康检查
            │   ├── 根据驱动类型执行命令
            │   └── 超时控制
            └── conn.recordHealthCheck(success, err) # 记录结果
                ├── conn.mu.Lock()
                ├── 更新健康状态
                ├── classifyHealthCheckError(err)
                └── conn.mu.Unlock()
```

### 4. 连接重建路径

```
markConnectionForRebuildAsync(pool, connID, conn)
    └── asyncRebuildConnection(pool, oldID, oldConn)
        ├── oldConn.isInUse()                  # 检查连接是否在使用
        ├── pool.mu.Lock()                     # 池锁
        ├── oldConn.beginRebuildWithLock()     # 连接锁（带超时）
        │   ├── 尝试获取conn.mu.Lock()（100ms超时）
        │   └── transitionStateLocked(StateRebuilding)
        ├── createConnection(p.ctx, pool)      # 创建新连接（持有锁！）
        ├── 替换连接（delete + add）
        ├── 更新统计
        ├── 发送事件
        ├── oldConn.transitionStateLocked(StateIdle)
        ├── oldConn.mu.Unlock()
        ├── pool.mu.Unlock()
        └── 异步关闭旧连接
            ├── oldConn.driver.Close()
            └── oldConn.completeClose()
```

### 5. 连接清理路径

```
cleanupTask()
    └── cleanupConnections()
        ├── 遍历所有协议池
        ├── pool.mu.RLock()                    # 池读锁（收集连接ID）
        ├── pool.mu.RUnlock()
        ├── 遍历连接检查是否需要清理
        ├── conn.tryAcquire()                  # 尝试获取连接
        ├── pool.mu.Lock()                     # 池写锁（删除连接）
        ├── delete(pool.connections, id)
        └── pool.mu.Unlock()
```

## 关键函数职责分析

### 1. `tryGetIdleConnection` - 尝试获取空闲连接
**调用者**: `getConnectionFromPool`
**被调用者**:
- `conn.tryAcquire()` - 获取连接锁
- `conn.isHealthy()` - 检查健康状态
- `shouldRebuildConnection()` - 检查是否需要重建
- `markConnectionForRebuildAsync()` - 标记重建

**问题**: 包含健康检查和重建检查，职责不专一

### 2. `shouldRebuildConnection` - 判断是否需要重建
**调用者**: `tryGetIdleConnection`
**被调用者**:
- `conn.getStatus()` - 获取连接状态
- `conn.isInUse()` - 检查是否在使用
- `conn.isHealthy()` - 检查健康状态
- `conn.isMarkedForRebuild()` - 检查是否已标记

**问题**: 上帝函数，包含开关检查、频率控制、状态检查、健康检查、策略判断等

### 3. `asyncRebuildConnection` - 异步重建连接
**调用者**: `markConnectionForRebuildAsync`
**被调用者**:
- `oldConn.isInUse()` - 检查使用状态
- `pool.mu.Lock()` - 获取池锁
- `oldConn.beginRebuildWithLock()` - 获取连接锁
- `createConnection()` - 创建新连接（阻塞操作）
- 各种状态转换和统计更新

**问题**: 在持有锁的情况下执行`createConnection`，可能长时间阻塞

### 4. `checkConnectionHealth` - 检查连接健康
**调用者**: `performHealthChecks`
**被调用者**:
- `conn.beginHealthCheck()` - 开始健康检查
- `defaultHealthCheck()` - 执行健康检查
- `conn.recordHealthCheck()` - 记录结果
- `classifyHealthCheckError()` - 分类错误

**问题**: 职责相对专一，但错误处理逻辑复杂

## 锁使用分析

### 三层锁结构
1. **池级锁** (`EnhancedConnectionPool.mu`): 保护协议工厂和驱动池映射
2. **驱动池锁** (`EnhancedDriverPool.mu`): 保护连接映射和池状态
3. **连接锁** (`EnhancedPooledConnection.mu`): 保护单个连接状态和属性

### 锁顺序问题
- `tryGetIdleConnection`: `conn.mu.Lock()` → `pool.mu.Lock()`（违反统一顺序）
- `asyncRebuildConnection`: `pool.mu.Lock()` → `conn.mu.Lock()`（正确顺序）
- `tryCreateNewConnection`: `pool.mu.Lock()` → 可能`conn.mu.Lock()`（潜在死锁）

### 锁内阻塞操作
- `asyncRebuildConnection`: 在持有`pool.mu`和`conn.mu`的情况下调用`createConnection`
- `createConnection`: 可能执行网络IO，长时间阻塞

## 依赖关系分析

### 强依赖（必须一起提取）
1. **状态机相关**:
   - `ConnectionState`枚举
   - `canTransitionTo()`函数
   - `transitionStateLocked()`函数
   - 状态转换规则

2. **健康检查相关**:
   - `HealthStatus`枚举
   - `checkConnectionHealth()`函数
   - `recordHealthCheck()`函数
   - `defaultHealthCheck()`函数
   - `classifyHealthCheckError()`函数

3. **重建决策相关**:
   - `shouldRebuildConnection()`函数
   - 重建策略逻辑
   - 重建标记管理

### 弱依赖（可以独立提取）
1. **连接包装器**:
   - `EnhancedPooledConnection`结构体
   - `tryAcquire()`、`release()`方法
   - 元数据管理方法

2. **事件处理**:
   - `sendEvent()`函数
   - `handleEvent()`函数
   - 事件类型定义

## 重构建议

### 提取优先级
1. **高优先级**: 状态机、健康检查、重建决策（解决职责重合问题）
2. **中优先级**: 连接包装器（减少单文件大小）
3. **低优先级**: 事件处理、错误处理（提高代码质量）

### 提取顺序
1. 状态机（依赖最少）
2. 健康检查管理器（依赖状态机）
3. 重建决策器（依赖健康检查）
4. 连接包装器（依赖状态机）
5. 其他模块

## 调用统计

| 函数 | 被调用次数 | 调用者数量 | 复杂度 |
|------|------------|------------|--------|
| `tryGetIdleConnection` | 1 | 1 | 高 |
| `shouldRebuildConnection` | 1 | 1 | 极高 |
| `asyncRebuildConnection` | 1 | 1 | 高 |
| `checkConnectionHealth` | N（连接数） | 1 | 中 |
| `createConnection` | 2 | 2 | 低 |
| `tryAcquire` | N（尝试次数） | 2 | 低 |
| `release` | N（释放次数） | 2 | 低 |

## 风险点

### 高风险
1. `asyncRebuildConnection`中的锁内阻塞操作
2. `beginRebuildWithLock`中的goroutine泄漏风险
3. `tryGetIdleConnection`中的锁顺序不一致

### 中风险
1. `shouldRebuildConnection`函数过于复杂
2. 健康检查与重建逻辑重合
3. 错误处理不一致

### 低风险
1. 单文件过大，代码可读性差
2. 函数职责不专一
3. 缺乏模块化设计

---

**文档更新**: 2025-12-23
**分析完成**: 第一阶段依赖关系分析