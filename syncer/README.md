# Syncer 包

Zabbix DDL Monitor 的配置同步包。

## 概述

syncer 包负责处理：
- 从 Zabbix API 同步配置信息
- 配置变更的事件驱动通知
- 支持并发访问的线程安全操作
- 基于订阅的事件传递系统

## 快速开始

```bash
# 运行所有测试
make test

# 运行并生成覆盖率报告
make test-coverage

# 运行并检测竞态条件
make test-race

# 运行特定类别的测试
make test-unit
make test-integration
make test-concurrency
```

## 测试结构

### 测试文件
- `syncer_test.go` - 核心单元测试
- `types_test.go` - 数据类型测试
- `integration_test.go` - 端到端测试
- `concurrency_test.go` - 线程安全测试
- `error_handling_test.go` - 错误场景测试
- `performance_test.go` - 性能基准测试

### 测试分类
- **单元测试** - 核心功能、生命周期管理
- **集成测试** - 完整工作流程场景
- **并发测试** - 竞态条件、线程安全
- **错误测试** - 网络、API 和系统错误
- **性能测试** - 可扩展性和基准测试

## 可用的 Make 命令

```bash
make test              # 所有测试
make test-unit         # 仅单元测试
make test-integration  # 集成测试
make test-concurrency  # 并发测试
make test-error-handling # 错误处理测试
make test-performance  # 性能测试
make test-bench        # 基准测试
make test-coverage     # 覆盖率报告
make test-race         # 竞态条件检测
make clean            # 清理构件
```

## 测试覆盖率

- **行覆盖率**: >95%
- **分支覆盖率**: >90%
- **函数覆盖率**: 公共 API 100%

## 依赖项

- `github.com/stretchr/testify` - 测试框架
- `github.com/charlesren/zapix` - Zabbix API 客户端

## 示例测试

```go
func TestExample(t *testing.T) {
    mockClient := &MockClient{}
    syncer := createTestSyncer(mockClient, time.Minute)
    
    mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
    mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)
    
    err := syncer.sync()
    assert.NoError(t, err)
    mockClient.AssertExpectations(t)
}
```

详细实现请参阅各个测试文件。