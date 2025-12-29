package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/charlesren/userconfig"
	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/manager"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
	"github.com/charlesren/zapix"
	"github.com/spf13/viper"
)

var (
	zc         *zapix.ZabbixClient
	UserConfig *viper.Viper
	ConfPath   string
)

func init() {
	confPath := flag.String("c", "../conf/svr.yml", "ConfigPath")
	flag.Parse()
	ConfPath = *confPath

	initConfig()
}

func initConfig() {
	var err error
	if UserConfig, err = userconfig.NewUserConfig(userconfig.WithPath(ConfPath)); err != nil {
		fmt.Printf("####LOAD_CONFIG_ERROR: %v", err)
		os.Exit(-1)
	}
	initLog()
	initZabbix()
}

func initLog() {
	logLevel := UserConfig.GetInt("server.log.applog.loglevel")

	// 支持通过环境变量覆盖日志级别 LOG_LEVEL=-1/0/1/2/3
	// -1=Trace, 0=Debug, 1=Info, 2=Warn, 3=Error
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		if level, err := strconv.Atoi(envLevel); err == nil {
			if level >= -1 && level <= 3 {
				logLevel = level
				fmt.Printf("从环境变量设置日志级别: %d\n", logLevel)
			} else {
				fmt.Printf("环境变量LOG_LEVEL值[%d]超出范围[-1,3]，使用配置文件值: %d\n", level, logLevel)
			}
		}
	}

	logPath := "../logs/ddl_monitor.log"
	logger := ylog.NewYLog(
		ylog.WithLogFile(logPath),
		ylog.WithMaxAge(3),
		ylog.WithMaxSize(100),
		ylog.WithMaxBackups(3),
		ylog.WithLevel(logLevel),
	)
	ylog.InitLogger(logger)
}

func initZabbix() {
	username := UserConfig.GetString("zabbix.username")
	password := UserConfig.GetString("zabbix.password")
	serverip := UserConfig.GetString("zabbix.serverip")
	serverport := UserConfig.GetString("zabbix.serverport")

	// 创建Zabbix客户端
	zc = zapix.NewZabbixClient()

	if os.Getenv("DEBUG") == "on" {
		zc.SetDebug(true)
	}
	// All Zabbix API requests use the same base URL.
	ylog.Infof("Zabbix", "username is ：%v", username)
	ylog.Debugf("Zabbix", "password is ：%v", password)
	url := fmt.Sprintf("http://%v:%v/api_jsonrpc.php", serverip, serverport)
	zc.Client.SetBaseURL(fmt.Sprintf("http://%v:%v/api_jsonrpc.php", serverip, serverport))
	err := zc.Login(url, username, password)
	if err != nil {
		ylog.Errorf("Zabbix", "login err: %v", err)
		return
	}
	ylog.Infof("Zabbix", "login success")
}

func main() {
	// 全局panic恢复 - 防止程序因未捕获的panic而崩溃
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("Main", "程序发生未捕获的panic: %v", r)
			ylog.Errorf("Main", "堆栈信息:\n%s", debug.Stack())
			fmt.Fprintf(os.Stderr, "程序发生未捕获的panic: %v\n", r)
			fmt.Fprintf(os.Stderr, "堆栈信息:\n%s\n", debug.Stack())

			// 给已有的defer一点时间执行资源清理
			ylog.Errorf("Main", "尝试执行资源清理...")
			time.Sleep(2 * time.Second)

			os.Exit(1)
		}
	}()

	ylog.Infof("Main", "服务启动，配置文件: %s", ConfPath)
	ylog.Infof("Main", "Go版本: %s", runtime.Version())
	ylog.Infof("Main", "GOMAXPROCS: %d", runtime.GOMAXPROCS(0))
	ylog.Infof("Main", "NumCPU: %d", runtime.NumCPU())

	// 启动健康监控
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	defer monitorCancel()
	go monitorSystemHealth(monitorCtx)

	// 用来通过代理名称获取代理的id
	proxyname := UserConfig.GetString("zabbix.proxyname")
	ylog.Infof("Main", "using proxyname: %s", proxyname)
	// 用来配置数据接收的链接
	proxyIP := UserConfig.GetString("zabbix.proxyip")
	ylog.Infof("Main", "using proxyIP: %s", proxyIP)
	proxyPort := UserConfig.GetString("zabbix.proxyport")
	ylog.Infof("Main", "using proxyPort: %s", proxyPort)

	// 1. 初始化配置同步器
	syncer, err := syncer.NewConfigSyncer(zc, 4*time.Hour, proxyname)
	if err != nil {
		ylog.Errorf("Main", "创建配置同步器失败: %v", err)
		return
	}
	ylog.Infof("Main", "配置同步器初始化完成 (同步间隔: 4h)")
	// 使用安全启动，防止syncer中的panic导致程序崩溃
	go safeStart(syncer.Start, "syncer.Start")
	defer syncer.Stop()

	// 2. 初始化任务注册表
	registry := task.NewDefaultRegistry()
	ylog.Infof("Main", "任务注册表初始化完成")

	// 3. 初始化聚合器 - 5个worker，200倍worker数量的缓冲队列，满100个批量刷，10秒定时刷
	aggregator := task.NewAggregator(5, 100, 10*time.Second)
	aggregator.AddHandler(&task.LogHandler{})
	aggregator.AddHandler(&task.MetricsHandler{})

	// 4. 初始化Zabbix发送器
	zabbixSenderConfig := task.ZabbixSenderConfig{
		ProxyIP:           proxyIP,
		ProxyPort:         proxyPort,
		ConnectionTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		PoolSize:          2,
	}
	zabbixSender, err := task.NewZabbixSenderHandler(zabbixSenderConfig)
	if err != nil {
		ylog.Errorf("Main", "创建zabbix sender失败:%v", err)
		return
	}

	// 预暖连接池
	if err := zabbixSender.WarmupPool(); err != nil {
		ylog.Warnf("Main", "连接池预暖失败: %v", err)
	}

	// 记录连接池统计信息
	stats := zabbixSender.GetStats()
	ylog.Infof("Main", "Zabbix sender连接池统计: %+v", stats)

	aggregator.AddHandler(zabbixSender)
	aggregator.Start()
	defer aggregator.Stop()
	defer func() {
		if err := zabbixSender.Close(); err != nil {
			ylog.Errorf("Main", "关闭zabbix sender失败: %v", err)
		} else {
			ylog.Infof("Main", "Zabbix sender已关闭")
		}
	}()

	// 5. 初始化管理器
	mgr := manager.NewManager(syncer, registry, aggregator)
	ylog.Infof("Main", "管理器初始化完成")
	mgr.Start()
	ylog.Infof("Main", "管理器启动完成")
	defer mgr.Stop()

	// 6. 设置信号处理 - 支持优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	ylog.Infof("Main", "信号监听已启动 (SIGINT/SIGTERM/SIGQUIT)")

	// 7. 记录启动完成信息
	ylog.Infof("Main", "所有组件初始化完成，服务已就绪")
	ylog.Infof("Main", "开始长期运行...")

	// 8. 等待终止信号 - 长期运行，直到收到退出信号
	sig := <-sigChan
	ylog.Infof("Main", "接收到终止信号 %v，开始优雅关闭...", sig)

	// 9. 在关闭前记录最终统计信息
	finalStats := zabbixSender.GetStats()
	ylog.Infof("Main", "最终Zabbix sender连接池统计: %+v", finalStats)

	// 10. 记录程序退出信息
	ylog.Infof("Main", "程序优雅退出")
}

// safeStart 安全启动goroutine，带有panic恢复和重试机制
func safeStart(fn func(), name string) {
	go func() {
		// 为每个goroutine单独设置panic恢复
		defer func() {
			if r := recover(); r != nil {
				ylog.Errorf("SafeStart", "%s发生panic: %v", name, r)
				ylog.Errorf("SafeStart", "%s堆栈信息:\n%s", name, debug.Stack())

				// 对于关键组件，可以考虑重启逻辑
				if name == "syncer.Start" {
					ylog.Warnf("SafeStart", "%s发生panic，考虑重启该组件", name)
					// 这里可以添加重启逻辑，但要注意避免无限重启循环
				}
			}
		}()

		ylog.Debugf("SafeStart", "启动组件: %s", name)
		fn()
		ylog.Debugf("SafeStart", "组件退出: %s", name)
	}()
}

// monitorSystemHealth 监控系统健康状态
func monitorSystemHealth(ctx context.Context) {
	ylog.Infof("HealthMonitor", "启动系统健康监控")
	defer ylog.Infof("HealthMonitor", "停止系统健康监控")

	ticker := time.NewTicker(60 * time.Second) // 每60秒检查一次
	defer ticker.Stop()

	var lastGoroutineCount int
	leakThreshold := 100 // goroutine泄漏阈值
	leakStartTime := time.Time{}
	leakDetected := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. 监控goroutine数量
			currentGoroutines := runtime.NumGoroutine()

			// 首次记录
			if lastGoroutineCount == 0 {
				lastGoroutineCount = currentGoroutines
				ylog.Infof("HealthMonitor", "初始goroutine数量: %d", currentGoroutines)
				continue
			}

			// 检查goroutine泄漏
			increase := currentGoroutines - lastGoroutineCount
			if increase > leakThreshold {
				if !leakDetected {
					leakDetected = true
					leakStartTime = time.Now()
					ylog.Warnf("HealthMonitor", "检测到goroutine泄漏迹象: 当前=%d, 上次=%d, 增长=%d",
						currentGoroutines, lastGoroutineCount, increase)
				} else {
					// 泄漏持续中
					duration := time.Since(leakStartTime)
					ylog.Warnf("HealthMonitor", "goroutine泄漏持续中: 持续=%v, 当前=%d, 总增长=%d",
						duration, currentGoroutines, currentGoroutines-lastGoroutineCount)

					// 泄漏持续超过5分钟，记录详细堆栈
					if duration > 5*time.Minute {
						buf := make([]byte, 2*1024*1024) // 2MB buffer
						n := runtime.Stack(buf, true)
						ylog.Errorf("HealthMonitor", "goroutine泄漏详细堆栈:\n%s", buf[:n])

						// 重置泄漏检测，避免重复记录
						leakDetected = false
					}
				}
			} else {
				if leakDetected {
					leakDetected = false
					ylog.Infof("HealthMonitor", "goroutine泄漏迹象消失")
				}
			}

			lastGoroutineCount = currentGoroutines

			// 2. 监控内存使用
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			// 记录内存使用情况（每小时记录一次详细情况）
			if time.Now().Minute() == 0 { // 每小时整点记录详细内存信息
				ylog.Infof("HealthMonitor", "内存详细状态:")
				ylog.Infof("HealthMonitor", "  Alloc: %v MiB (当前分配)", m.Alloc/1024/1024)
				ylog.Infof("HealthMonitor", "  TotalAlloc: %v MiB (累计分配)", m.TotalAlloc/1024/1024)
				ylog.Infof("HealthMonitor", "  Sys: %v MiB (系统内存)", m.Sys/1024/1024)
				ylog.Infof("HealthMonitor", "  HeapAlloc: %v MiB (堆内存)", m.HeapAlloc/1024/1024)
				ylog.Infof("HealthMonitor", "  HeapSys: %v MiB (堆系统内存)", m.HeapSys/1024/1024)
				ylog.Infof("HealthMonitor", "  HeapIdle: %v MiB (堆空闲)", m.HeapIdle/1024/1024)
				ylog.Infof("HealthMonitor", "  HeapInuse: %v MiB (堆使用中)", m.HeapInuse/1024/1024)
				ylog.Infof("HealthMonitor", "  NumGC: %d (GC次数)", m.NumGC)

				// 检查内存泄漏迹象
				if m.HeapInuse > 1024*1024*1024 { // 超过1GB
					ylog.Warnf("HealthMonitor", "堆内存使用较高: %v MiB", m.HeapInuse/1024/1024)
				}
			} else {
				// 普通情况下只记录概要
				ylog.Infof("HealthMonitor", "系统状态: goroutines=%d, memory=%v MiB, GC=%d",
					currentGoroutines, m.Alloc/1024/1024, m.NumGC)
			}

			// 3. 监控GC状态
			if m.NumGC > 0 {
				lastGC := time.Unix(0, int64(m.LastGC))
				elapsed := time.Since(lastGC)

				// 如果超过10分钟没有GC，可能是内存使用稳定
				if elapsed > 10*time.Minute {
					ylog.Debugf("HealthMonitor", "距离上次GC已过: %v", elapsed)
				}

				// 检查GC频率是否异常
				if m.NumGC > 1000 { // GC次数过多
					ylog.Warnf("HealthMonitor", "GC次数较多: %d", m.NumGC)
				}
			}
		}
	}
}
