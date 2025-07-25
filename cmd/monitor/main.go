package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/task"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/aggregator"
	"github.com/charlesren/zabbix_ddl_monitor/manager"
)

var zc = zapix.NewZabbixClient()
var UserConfig *viper.Viper
var ConfPath string

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
	logPath := "../logs/setupAppProcessPortMonitor.log"
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
	// 通过config解析 proxy ip
	proxyIP := UserConfig.GetString("server.ip")
	// 根据proxy ip,通过zabbix api  获取proxy  id
	// todo
	//  获取绑定到proxy ip 的主机
	// todo
	
	// 初始化组件
	taskReg := task.NewRegistry()
	taskReg.Register("ping", &task.PingTask{})

	connPool := connection.NewConnectionPool()
	aggregator := aggregator.New()
	// Note: 创建manager实例来管理任务，而不是直接使用scheduler
	mgr, err := manager.NewManager(
		UserConfig.GetString("zabbix.serverip"), 
		UserConfig.GetString("zabbix.username"), 
		UserConfig.GetString("zabbix.password"))
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}
	mgr.Start()

	// 优雅退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	mgr.Stop()
	log.Println("服务已停止")
}
