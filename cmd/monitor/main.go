package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	ylog.Infof("Main", "服务启动，配置文件: %s", ConfPath)
	//用来通过代理名称获取代理的id
	proxyname := UserConfig.GetString("zabbix.proxyname")
	ylog.Infof("Main", "using proxyname: %s", proxyname)
	//用来配置数据接收的链接
	proxyIP := UserConfig.GetString("zabbix.proxyip")
	ylog.Infof("Main", "using proxyIP: %s", proxyIP)
	proxyPort := UserConfig.GetString("zabbix.proxyport")
	ylog.Infof("Main", "using proxyPort: %s", proxyPort)

	syncer, err := syncer.NewConfigSyncer(zc, 10*time.Minute, proxyname)
	if err != nil {
		ylog.Errorf("Main", "创建配置同步器失败: %v", err)
		return
	}
	ylog.Infof("Main", "配置同步器初始化完成 (同步间隔: 5m)")
	go syncer.Start()
	defer syncer.Stop()

	registry := task.NewDefaultRegistry()
	ylog.Infof("Main", "任务注册表初始化完成")

	aggregator := task.NewAggregator(5, 500, 15*time.Second)
	aggregator.AddHandler(&task.LogHandler{})
	aggregator.AddHandler(&task.MetricsHandler{})
	zabbixSenderConfig := task.ZabbixSenderConfig{
		ProxyIP:           proxyIP,
		ProxyPort:         proxyPort,
		ConnectionTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		PoolSize:          5,
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

	// 创建管理器
	mgr := manager.NewManager(syncer, registry, aggregator)
	ylog.Infof("Main", "管理器初始化完成")
	mgr.Start()
	ylog.Infof("Main", "管理器启动完成")
	defer mgr.Stop()

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ylog.Infof("Main", "信号监听已启动 (SIGINT/SIGTERM)")
	// 等待终止信号
	<-sigChan
	ylog.Infof("Main", "接收到终止信号，开始优雅关闭...")

	// 在关闭前记录最终统计信息
	finalStats := zabbixSender.GetStats()
	ylog.Infof("Main", "最终Zabbix sender连接池统计: %+v", finalStats)
}
