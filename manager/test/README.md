# Manager测试文档

## 概述

本目录包含了专线监控系统Manager模块的完整测试套件，包括单元测试、集成测试、性能测试和压力测试。

## 测试文件结构

```
manager/
├── manager.go              # Manager主实现
├── manager_test.go         # Manager单元测试
├── router_scheduler.go     # RouterScheduler实现
├── router_scheduler_test.go # RouterScheduler测试
├── interval_queue.go       # IntervalTaskQueue实现
├── interval_queue_test.go  # IntervalTaskQueue测试
├── integration_test.go     # 集成测试
└── test/
    ├── README.md          # 本文档
    └── run_tests.sh       # 测试运行脚本
```

## 测试覆盖范围

### 1. Manager测试 (manager_test.go)

- **基础功能测试**
  - Manager创建和初始化
  - PingTask注册（成功/失败）
  - 全量同步功能
  - 调度器管理

- **事件处理测试**
  - 专线创建事件
  - 专线更新事件
  - 专线删除事件
  - 重复事件处理
  - 延迟清理机制

- **并发测试**
  - 并发专线事件处理
  - 快速变更场景
  - 竞态条件检测

- **性能测试**
  - 大量专线处理
  - 事件处理性能基准

### 2. RouterScheduler测试 (router_scheduler_test.go)

- **基础功能测试**
  - 调度器创建和初始化
  - 队列初始化
  - 连接池预热

- **专线操作测试**
  - 专线创建/更新/删除
  - 队列间专线迁移
  - 专线重置功能

- **任务执行测试**
  - 任务发现和构建
  - 命令执行和结果解析
  - 错误处理

- **并发测试**
  - 并发专线操作
  - 线程安全验证

### 3. IntervalTaskQueue测试 (interval_queue_test.go)

- **基础功能测试**
  - 队列创建和配置
  - 任务添加/移除/更新
  - 任务快照获取

- **调度测试**
  - 定时执行信号
  - 非阻塞通知机制
  - 队列停止功能

- **并发测试**
  - 并发访问安全性
  - 大量任务处理
  - 批量替换操作

- **性能测试**
  - 大数据集处理
  - 操作性能基准

### 4. 集成测试 (integration_test.go)

- **真实依赖集成**
  - Manager与ConfigSyncer集成
  - Manager与RouterScheduler集成
  - 完整工作流测试

- **性能测试**
  - 大规模场景处理
  - 启动时间测试

- **压力测试**
  - 快速变更处理
  - 系统稳定性验证

- **错误恢复测试**
  - 异常数据处理
  - 系统恢复能力

## 运行测试

### 运行所有测试
```bash
cd zabbix_ddl_monitor/manager
go test -v ./...
```

### 运行特定测试文件
```bash
# 单独运行Manager测试
go test -v -run TestManager manager_test.go

# 运行RouterScheduler测试
go test -v -run TestRouterScheduler router_scheduler_test.go

# 运行IntervalTaskQueue测试
go test -v -run TestIntervalTaskQueue interval_queue_test.go
```

### 运行集成测试
```bash
go test -v -run Integration integration_test.go
```

### 运行性能基准测试
```bash
go test -v -bench=. -benchmem
```

### 跳过长时间运行的测试
```bash
go test -v -short
```

### 生成测试覆盖率报告
```bash
go test -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

## 测试工具和Mock

### Mock对象
- **MockConfigSyncer**: 模拟配置同步器
- **MockRegistry**: 模拟任务注册中心
- **MockScheduler**: 模拟路由器调度器
- **MockConnectionPool**: 模拟连接池
- **MockProtocolDriver**: 模拟协议驱动
- **MockTask**: 模拟任务实现
- **MockZabbixClient**: 模拟Zabbix客户端

### 测试助手函数
- `createTestLine()`: 创建测试专线
- `createTestRouter()`: 创建测试路由器
- `createTestManager()`: 创建测试Manager
- `setupMockConnectionPool()`: 设置模拟连接池
- `setupMockTask()`: 设置模拟任务

## 测试场景

### 正常场景
1. 系统启动和初始化
2. 专线配置同步
3. 调度器创建和管理
4. 任务队列调度
5. 系统优雅停止

### 异常场景
1. 注册失败处理
2. 连接池错误
3. 任务执行失败
4. 无效专线数据
5. 系统资源不足

### 边界场景
1. 空专线配置
2. 大量专线处理
3. 零间隔队列
4. 重复专线ID
5. 并发访问冲突

### 性能场景
1. 10,000+专线处理
2. 快速配置变更
3. 高并发访问
4. 内存使用优化
5. 响应时间要求

## 测试数据

### 测试专线配置
- 路由器IP: 192.168.1.1 - 192.168.10.1
- 专线IP: 10.0.0.1 - 10.255.255.255
- 检查间隔: 1分钟 - 60分钟
- 平台: cisco_iosxe, huawei_vrp, h3c_comware
- 协议: ssh, netconf, scrapli-channel

### Mock数据生成
```go
// 创建测试专线
line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)

// 创建大量测试数据
for i := 0; i < 1000; i++ {
    line := createTestLine(
        fmt.Sprintf("line%d", i),
        fmt.Sprintf("10.0.%d.%d", i/256, i%256),
        fmt.Sprintf("192.168.%d.1", (i%10)+1),
        time.Duration(i%10+1)*time.Minute,
    )
}
```

## 调试和故障排除

### 常见问题

1. **测试超时**
   - 检查测试中的sleep时间
   - 确保及时调用defer cleanup函数
   - 考虑使用shorter test flag

2. **竞态条件**
   - 使用go test -race检测
   - 检查共享状态的锁保护
   - 验证channel的正确使用

3. **Mock期望不匹配**
   - 检查mock.On()调用的参数
   - 确保所有mock期望都被满足
   - 使用mock.AssertExpectations()验证

4. **内存泄漏**
   - 确保goroutine正确退出
   - 检查channel是否被关闭
   - 使用defer释放资源

### 调试技巧

1. **详细日志**
   ```go
   t.Logf("Debug info: %+v", someValue)
   ```

2. **条件断点**
   ```go
   if condition {
       t.Fatalf("Unexpected condition: %v", value)
   }
   ```

3. **状态检查**
   ```go
   manager.mu.Lock()
   t.Logf("Schedulers: %d, Lines: %d", len(manager.schedulers), len(manager.routerLines))
   manager.mu.Unlock()
   ```

## 性能指标

### 期望性能指标
- 1000专线启动时间: < 5秒
- 专线事件处理: < 10ms
- 内存使用: < 100MB (1000专线)
- 并发处理: 支持100+并发操作

### 基准测试结果示例
```
BenchmarkManager_ProcessLineCreate-8     	   50000	     25000 ns/op	    2048 B/op	      10 allocs/op
BenchmarkRouterScheduler_OnLineCreated-8 	  100000	     15000 ns/op	    1024 B/op	       5 allocs/op
BenchmarkIntervalTaskQueue_Add-8         	 1000000	      1000 ns/op	     256 B/op	       2 allocs/op
```

## 持续集成

### CI/CD Pipeline
```yaml
test:
  script:
    - go test -v -race -coverprofile=coverage.out ./...
    - go tool cover -func=coverage.out
    - go test -v -bench=. -benchmem
  coverage: '/coverage: \d+\.\d+% of statements/'
```

### 质量门禁
- 测试覆盖率 > 80%
- 所有测试必须通过
- 无竞态条件检测
- 性能基准不退化

## 贡献指南

### 添加新测试
1. 遵循现有测试命名约定
2. 包含正常/异常/边界场景
3. 添加适当的文档注释
4. 确保mock对象正确清理

### 测试最佳实践
1. 每个测试应该独立可运行
2. 使用table-driven tests处理多场景
3. 适当使用并行测试提高效率
4. 保持测试简洁和可读性

### 代码审查清单
- [ ] 测试覆盖所有代码路径
- [ ] Mock对象使用正确
- [ ] 资源正确释放
- [ ] 没有硬编码延时
- [ ] 错误处理完整
- [ ] 文档注释完善