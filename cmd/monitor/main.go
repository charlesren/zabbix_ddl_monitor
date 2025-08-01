package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/charlesren/userconfig"
	"github.com/charlesren/ylog"
	"github.com/charlesren/zapix"
	"github.com/spf13/viper"



)
	var (
	zc      *zapix.NewZabbixClient()
	UserConfig *viper.Viper
	ConfPath string
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

ylog.Infof("Main","Service started with config:%s"，ConfPath)
	// 通过config解析 proxy ip
	proxyIP := UserConfig.GetString("server.ip")
	// 根据proxy ip,通过zabbix api  获取proxy  id
	// todo
	//  获取绑定到proxy ip 的主机
	// todo


}
