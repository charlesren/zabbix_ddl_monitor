
1. 核心架构分层

MonitorService (main.go)
├── ConfigSyncer (配置同步)
├── SchedulerManager (总调度器)
│   ├── RouterScheduler (单路由器调度器)
│   │   ├── IntervalTaskQueue (间隔队列)
│   │   └── RouterConnection (专属连接)
│   └── RouterCache (路由器信息缓存)
├── TaskRegistry (任务注册中心)
└── Aggregator (结果聚合上报)


graph TD
    A[MonitorService] --> B[ConfigSyncer]
    A --> C[SchedulerManager]
    B -->|配置变更通知| C
    C --> D[RouterScheduler1]
    C --> E[RouterScheduler2]
    D --> F[IntervalQueue-30s]
    D --> G[IntervalQueue-60s]
    E --> H[IntervalQueue-5m]
    D --> I[RouterConnection]
    E --> J[RouterConnection]
    F/K --> L[Aggregator]
    G/M --> L
    H/N --> L
    L -->|批量上报| Zabbix



2. 关键组件职责

关键组件职责**

| **组件**               | **职责**                                                                 | **核心字段/方法**                                                                 |
|------------------------|--------------------------------------------------------------------------|---------------------------------------------------------------------------------|
| **ConfigSyncer**       | 从Zabbix API定时同步专线配置                                              | `sync()` `fetchLines()` `Subscribe()`                                           |
| **SchedulerManager**   | 全局调度协调，维护所有路由器的调度器实例                                   | `updateSchedulers()` `routerCache`                                              |
| **RouterScheduler**    | 单个路由器的任务调度，管理多个间隔队列和专属连接                           | `AddLine()` `runPendingTasks()` `connection`                                    |
| **IntervalTaskQueue**  | 按固定间隔执行专线检查任务，支持批量操作                                   | `ShouldRun()` `Execute()`                                                       |
| **RouterConnection**   | 封装路由器连接的生命周期管理（创建/保活/销毁）                             | `Get()` `Close()` `lastUsed`                                                    |
| **TaskRegistry**       | 注册和获取平台特定的任务实现（如Ping）                                     | `Register()` `Get()`                                                            |
| **Aggregator**         | 批量压缩和上报检测结果                                                     | `Report()` `ResultCompressor`                                                   |

---

### **3. 数据流

sequenceDiagram
    participant ZabbixAPI
    participant ConfigSyncer
    participant SchedulerManager
    participant RouterScheduler
    participant Router
    participant Aggregator

    loop 定时同步
        ConfigSyncer->>ZabbixAPI: 拉取专线配置
        ZabbixAPI-->>ConfigSyncer: 返回Line列表
        ConfigSyncer->>SchedulerManager: 通知变更
    end

    SchedulerManager->>RouterScheduler: 创建/更新队列
    loop 每秒触发
        RouterScheduler->>IntervalTaskQueue: 检查到期任务
        IntervalTaskQueue->>Router: 批量执行命令
        Router-->>IntervalTaskQueue: 返回结果
        IntervalTaskQueue->>Aggregator: 压缩上报
    end


4. 设计亮点**
1. **连接管理优化**
   - 每个 `RouterScheduler` 持有独立 `RouterConnection`
   - 心跳机制自动检测失效连接（`lastUsed` 时间戳）
   - 避免全局连接池的锁竞争

2. **动态调度能力**
   - 配置变更时动态调整 `IntervalTaskQueue`
   - 自动清理无专线的路由器调度器

3. **平台适配性**
   - 通过 `TaskRegistry` 支持不同平台的命令生成和结果解析
   - 新增平台只需注册对应 `Task` 实现

4. **资源隔离**
   - 错误仅限于单个路由器调度器内
   - 慢任务不会阻塞其他路由器的检测
