package main

import (
	"automation/common/userconfig"
	"automation/common/ylog"
	"automation/common/zabbix"
	"flag"
	"fmt"
	"github.com/spf13/viper"
	"github.com/xuri/excelize/v2"
	"os"
	"strings"
)

var zc = zabbix.NewZabbixClient()
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

	//TEMP_APP_PROCESS_PORT_BASE  tempid
	basetempname := "TEMP_APP_PROCESS_PORT_BASE"
	var basetempid string
	basetemp, err := zc.GetTemplateFormTechnicalName(basetempname)
	if err != nil {
		ylog.Errorf("Zabbix", "get template  %v error: %v", basetempname, err)
	}
	if len(basetemp) == 0 {
		ylog.Infof("Zabbix", "template %v not found", basetempname)

		basetempTags := []zabbix.TemplateTagObject{{
			Tag:   "TempType",
			Value: "APP",
		}}

		basetempGroups := []zabbix.HostgroupObject{
			{
				GroupID: "1",
			},
		}

		basetempMacros := []zabbix.UsermacroObject{
			{
				Macro:       "{$APP_NAME}",
				Value:       "",
				Description: "应用系统名称",
			},
			{
				Macro:       "{$APP_PORT_NUMBERS}",
				Value:       "",
				Description: "应用端口号，多个端口号以|分隔，如8080|8443",
			},
			{
				Macro:       "{$APP_PORT_NUMBERS2}",
				Value:       "",
				Description: "如{$APP_PORT_NUMBERS}的值超过255个支符，可在此补充，格式相同",
			},
			{
				Macro:       "{$APP_PORT_NUMBERS3}",
				Value:       "",
				Description: "如{$APP_PORT_NUMBERS}的值超过255个支符，可在此补充，格式相同",
			},
			{
				Macro:       "{$APP_PROCESSES}",
				Value:       "",
				Description: "应用进程关键字及数量，以#分隔；多个应用以|分隔，如zabbix_agentd#7|zabbix_serverd#1",
			},
			{
				Macro:       "{$APP_PROCESSES2}",
				Value:       "",
				Description: "如{$APP_PROCESSES}的值超过255个支符，可在此补充，格式相同",
			},
			{
				Macro:       "{$APP_PROCESSES3}",
				Value:       "",
				Description: "如{$APP_PROCESSES}的值超过255个支符，可在此补充，格式相同",
			},
		}

		basetmp := zabbix.TemplateObject{}
		basetmp.Host = basetempname
		basetmp.Description = "应用进程及端口监控基础模板"
		basetmp.Tags = basetempTags
		basetmp.Macros = basetempMacros
		basetmp.Groups = basetempGroups
		basetmps := []zabbix.TemplateObject{}
		basetmps = append(basetmps, basetmp)
		ts, err := zc.TemplateCreate(basetmps)
		if err != nil {
			ylog.Errorf("Zabbix", "create template:  %v with error: %v", basetempname, err)
		}
		if len(ts) == 0 {
			ylog.Infof("Zabbix", " template: %v not created ", basetempname)
			return
		}

		basetempid = ts[0]
		ylog.Infof("Zabbix", " template id for %v is %v", basetempname, basetempid)

		portlld := zabbix.LLDObject{}
		portlld.Name = "Application Port Number Discovery"
		portlld.Key = "parse_app_portnumbers[\"{$APP_PORT_NUMBERS}\",\"{$APP_PORT_NUMBERS2}\",\"{$APP_PORT_NUMBERS3}\"]"
		portlld.HostID = basetempid
		portlld.Type = 10
		portlld.Delay = "1h"

		portllds := []zabbix.LLDObject{}
		portllds = append(portllds, portlld)
		portrules, err := zc.LLDCreate(portllds)
		if err != nil {
			ylog.Errorf("Zabbix", "create lld :%v for template: %v with error: %v", portlld.Name, basetempname, err)
		}
		if len(portrules) == 0 {
			ylog.Infof("Zabbix", " lld :%v for %v not created ", portlld.Name, basetempname)
			return
		}
		portlldid := portrules[0]
		ylog.Infof("Zabbix", " lld id of %v for template %v is %v", portlld.Name, basetempname, portlldid)

		portitemproto := zabbix.ItemPrototypeObject{}
		portitemproto.Name = "[APP_PORT] {#APP_PORT_NUMBER}端口监听状态"
		portitemproto.Key = "net.tcp.listen[{#APP_PORT_NUMBER}]"
		portitemproto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Port"}}
		portitemproto.HostID = basetempid
		portitemproto.RuleID = portlldid
		portitemproto.Type = 0
		portitemproto.ValueType = 3
		portitemproto.Delay = "1m"
		portitemproto.Description = "端口监听状态"
		portitemprotos := []zabbix.ItemPrototypeObject{}
		portitemprotos = append(portitemprotos, portitemproto)
		portitems, err := zc.ItemPrototypeCreate(portitemprotos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype :%v for temp:  %v with error: %v", portitemproto.Name, basetempname, err)
		}
		if len(portitems) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", portitemproto.Name, basetempname)
			return
		}
		portitemid := portitems[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", portitemproto.Name, basetempname, portitemid)

		// creat processlld
		processlld := zabbix.LLDObject{}
		processlld.Name = "Application Process Discovery"
		processlld.Key = "parse_app_processes[\"{$APP_PROCESSES}\",\"{$APP_PROCESSES2}\",\"{$APP_PROCESSES3}\"]"
		processlld.HostID = basetempid
		processlld.Type = 10
		processlld.Delay = "1h"

		processllds := []zabbix.LLDObject{}
		processllds = append(processllds, processlld)
		processrules, err := zc.LLDCreate(processllds)
		if err != nil {
			ylog.Errorf("Zabbix", "create lld :%v for template: %v with error: %v", processlld.Name, basetempname, err)
		}
		if len(processrules) == 0 {
			ylog.Infof("Zabbix", " lld :%v for %v not created ", processlld.Name, basetempname)
			return
		}
		processlldid := processrules[0]
		ylog.Infof("Zabbix", " lld id of %v for template: %v is %v", processlld.Name, basetempname, processlldid)

		processitemproto := zabbix.ItemPrototypeObject{}
		processitemproto.Name = "[APP_PROCESS] {#APP_PROCESS_CMD_LINE}进程数量"
		processitemproto.Key = "proc.num[,,,{#APP_PROCESS_CMD_LINE}]"
		processitemproto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Process"}}
		processitemproto.HostID = basetempid
		processitemproto.RuleID = processlldid
		processitemproto.Type = 0
		processitemproto.ValueType = 3
		processitemproto.Delay = "1m"
		processitemproto.Description = "正在运行中的应用进程数"
		processitemprotos := []zabbix.ItemPrototypeObject{}
		processitemprotos = append(processitemprotos, processitemproto)
		processitems, err := zc.ItemPrototypeCreate(processitemprotos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype:%v for template: %v with error: %v", processitemproto.Name, basetempname, err)
		}
		if len(processitems) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", processitemproto.Name, basetempname)
			return
		}
		processitemid := processitems[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", processitemproto.Name, basetempname, processitemid)

		processitem2proto := zabbix.ItemPrototypeObject{}
		processitem2proto.Name = "[APP_PROCESS] 僵尸状态的{#APP_PROCESS_CMD_LINE}进程数量"
		processitem2proto.Key = "proc.num[,,zomb,{#APP_PROCESS_CMD_LINE}]"
		processitem2proto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Process"}}
		processitem2proto.HostID = basetempid
		processitem2proto.RuleID = processlldid
		processitem2proto.Type = 0
		processitem2proto.ValueType = 3
		processitem2proto.Delay = "1m"
		processitem2proto.Description = "僵尸状态的进程数量"
		processitem2protos := []zabbix.ItemPrototypeObject{}
		processitem2protos = append(processitem2protos, processitem2proto)
		processitem2s, err := zc.ItemPrototypeCreate(processitem2protos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype :%v for temp  %v error: %v", processitem2proto.Name, basetempname, err)
		}
		if len(processitem2s) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", processitem2proto.Name, basetempname)
			return
		}
		processitem2id := processitem2s[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", processitem2proto.Name, basetempname, processitem2id)

		processitem3proto := zabbix.ItemPrototypeObject{}
		processitem3proto.Name = "[APP_PROCESS] {#APP_PROCESS_CMD_LINE}进程内存使用量"
		processitem3proto.Key = "proc.mem[,,,{#APP_PROCESS_CMD_LINE},rss]"
		processitem3proto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Process"}}
		processitem3proto.HostID = basetempid
		processitem3proto.RuleID = processlldid
		processitem3proto.Type = 0
		processitem3proto.ValueType = 0
		processitem3proto.Units = "bytes"
		processitem3proto.Delay = "1m"
		processitem3proto.Description = "进程内存使用量"
		processitem3protos := []zabbix.ItemPrototypeObject{}
		processitem3protos = append(processitem3protos, processitem3proto)
		processitem3s, err := zc.ItemPrototypeCreate(processitem3protos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype :%v for temp  %v error: %v", processitem3proto.Name, basetempname, err)
		}
		if len(processitem3s) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", processitem3proto.Name, basetempname)
			return
		}
		processitem3id := processitem3s[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", processitem3proto.Name, basetempname, processitem3id)

		processitem4proto := zabbix.ItemPrototypeObject{}
		processitem4proto.Name = "[APP_PROCESS] {#APP_PROCESS_CMD_LINE}进程CPU使用率"
		processitem4proto.Key = "proc.cpu.util[,,,{#APP_PROCESS_CMD_LINE},]"
		processitem4proto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Process"}}
		processitem4proto.HostID = basetempid
		processitem4proto.RuleID = processlldid
		processitem4proto.Type = 0
		processitem4proto.ValueType = 0
		processitem4proto.Units = "%"
		processitem4proto.Delay = "1m"
		processitem4proto.Description = "进程CPU使用率"
		processitem4protos := []zabbix.ItemPrototypeObject{}
		processitem4protos = append(processitem4protos, processitem4proto)
		processitem4s, err := zc.ItemPrototypeCreate(processitem4protos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype :%v for temp  %v error: %v", processitem4proto.Name, basetempname, err)
		}
		if len(processitem4s) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", processitem4proto.Name, basetempname)
			return
		}
		processitem4id := processitem4s[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", processitem4proto.Name, basetempname, processitem4id)

		processitem5proto := zabbix.ItemPrototypeObject{}
		processitem5proto.Name = "[APP_PROCESS] {#APP_PROCESS_CMD_LINE}进程内存使用率"
		processitem5proto.Key = "proc.mem[,,,{#APP_PROCESS_CMD_LINE},pmem]"
		processitem5proto.ApplicationPrototypes = []zabbix.ApplicationPrototypeObject{{Name: "[APP] Process"}}
		processitem5proto.HostID = basetempid
		processitem5proto.RuleID = processlldid
		processitem5proto.Type = 0
		processitem5proto.ValueType = 0
		processitem5proto.Units = "%"
		processitem5proto.Delay = "1m"
		processitem5proto.Description = "进程内存使用率"
		processitem5protos := []zabbix.ItemPrototypeObject{}
		processitem5protos = append(processitem5protos, processitem5proto)
		processitem5s, err := zc.ItemPrototypeCreate(processitem5protos)
		if err != nil {
			ylog.Errorf("Zabbix", "create item prototype :%v for temp  %v error: %v", processitem5proto.Name, basetempname, err)
		}
		if len(processitem5s) == 0 {
			ylog.Infof("Zabbix", "prototype :%v for %v not created ", processitem5proto.Name, basetempname)
			return
		}
		processitem5id := processitem5s[0]
		ylog.Infof("Zabbix", "prototype :%v for %v created ,id: %v", processitem5proto.Name, basetempname, processitem5id)

		//create trigers

		baseTriggers := []zabbix.TriggerPrototypeObject{
			{
				Description: "[APP_PORT] {#APP_PORT_NUMBER}端口未被监听！",
				Opdata:      "端口：{#APP_PORT_NUMBER}，主机：{HOST.NAME}",
				Expression:  fmt.Sprintf("{%v:net.tcp.listen[{#APP_PORT_NUMBER}].last()}=0", basetempname),
				Priority:    4,
				Comments:    "端口未被监听",
				ManualClose: 1,
				Tags: []zabbix.TriggerPrototypeTagObject{
					{
						Tag:   "AlertType",
						Value: "APP",
					},
					{
						Tag:   "AppName",
						Value: "{$APP_NAME}",
					},
				},
			},
			{
				Description: "[APP_PROCESS] {#APP_PROCESS_CMD_LINE}进程数量异常！",
				Opdata:      "当前数量：{ITEM.LASTVALUE1} ，期望数量：{#APP_PROCESS_NUMBER}，主机：{HOST.NAME}",
				Expression:  fmt.Sprintf("{%v:proc.num[,,,{#APP_PROCESS_CMD_LINE}].last()}<{#APP_PROCESS_NUMBER}", basetempname),
				Priority:    4,
				Comments:    "进程数量少于预期",
				ManualClose: 1,
				Tags: []zabbix.TriggerPrototypeTagObject{
					{
						Tag:   "AlertType",
						Value: "APP",
					},
					{
						Tag:   "AppName",
						Value: "{$APP_NAME}",
					},
				},
			},
			{
				Description: "[APP_PROCESS] 存在僵尸进程{#APP_PROCESS_CMD_LINE}！",
				Opdata:      "进程名称：{#APP_PROCESS_CMD_LINE}，僵尸进程数量：{ITEM.LASTVALUE1}，主机：{HOST.NAME}",
				Expression:  fmt.Sprintf("{%v:proc.num[,,zomb,{#APP_PROCESS_CMD_LINE}].last()}>=1", basetempname),
				Priority:    4,
				Comments:    "存在僵尸状态的进程",
				ManualClose: 1,
				Tags: []zabbix.TriggerPrototypeTagObject{
					{
						Tag:   "AlertType",
						Value: "APP",
					},
					{
						Tag:   "AppName",
						Value: "{$APP_NAME}",
					},
				},
			},
		}
		tgs, err := zc.TriggerPrototypeCreate(baseTriggers)
		if err != nil {
			ylog.Errorf("Zabbix", "create triggers for temp %v error: %v", basetempname, err)
		}
		for _, tg := range tgs {
			ylog.Infof("Zabbix", " triggers %v created for temp %v", tg, basetempname)
		}

	} else {
		basetempid = basetemp[0].TemplateID
		ylog.Infof("Zabbix", "id for template %v is %v", basetempname, basetempid)
	}

	filename := "appProcessPortMonitorConfigTemplate.xlsx"
	f, err := excelize.OpenFile(filename)
	if err != nil {
		ylog.Errorf("Zabbix", "open file : %v with err: %v", filename, err)
		return
	}
	defer func() {
		// Close the spreadsheet.
		if err := f.Close(); err != nil {
			ylog.Errorf("Zabbix", "close file : %v with err: %v", filename, err)
		}
	}()
	// Get all the rows in the Sheet1.
	rows, err := f.GetRows("Sheet1")
	if err != nil {
		ylog.Errorf("Zabbix", "GetRows err: %v", err)
		return
	}
	tempTags := []zabbix.TemplateTagObject{{
		Tag:   "TempType",
		Value: "APP",
	}}

	tempGroups := []zabbix.HostgroupObject{
		{
			GroupID: "1",
		},
	}
	linkedtemps := []zabbix.TemplateObject{
		{
			TemplateID: basetempid,
		},
	}
	for _, row := range rows {
		if strings.Contains(row[0], "模板说明") {
			continue
		}
		if row[0] == "系统名称" {
			continue
		}
		for len(row) < 5{
			row = append(row,"")
		}
		appname := row[0]
		appcode := row[1]
		ips := row[2]
		processkeywords := row[3]
		portnumbers := row[4]

		tempMacros := []zabbix.UsermacroObject{

			{
				Macro:       "{$APP_NAME}",
				Value:       appname,
				Description: "应用系统名称",
			},
			{
				Macro:       "{$APP_PORT_NUMBERS}",
				Value:       "",
				Description: "应用端口号，多个端口号以|分隔，如8080|8443",
			},
			{
				Macro:       "{$APP_PROCESSES}",
				Value:       "",
				Description: "应用进程关键字及数量，以#分隔；多个应用以|分隔，如zabbix_agentd#7|zabbix_serverd#1",
			},
		}

		tempname := fmt.Sprintf("TEMP_APP_%v", appcode)
		// ts used to store template ids already bind to ip
		ts := []string{}
		temp, err := zc.GetTemplateFormTechnicalName(tempname)
		if err != nil {
			ylog.Errorf("Zabbix", "get temp  %v error: %v", tempname, err)
		}
		if len(temp) == 0 {
			ylog.Infof("Zabbix", "no temp nameed %v found", tempname)
			tmp := zabbix.TemplateObject{}
			tmp.Host = tempname
			tmp.Description = fmt.Sprintf("%v应用监控模板", appname)
			tmp.Templates = linkedtemps
			tmp.Tags = tempTags
			tmp.Macros = tempMacros
			tmp.Groups = tempGroups
			tmps := []zabbix.TemplateObject{}
			tmps = append(tmps, tmp)
			ts, err = zc.TemplateCreate(tmps)
			if err != nil {
				ylog.Errorf("Zabbix", "create template for app %v error: %v", appname, err)
				continue
			}
			for _, t := range ts {
				ylog.Infof("Zabbix", "template %v created for app %v ", t, appname)
			}
		} else {
			ylog.Infof("Zabbix", "template %v already exist for app %v ", tempname, appname)
			ts = []string{temp[0].TemplateID}
		}

		//define hostmacro
		appProcessMacro := zabbix.UsermacroObject{
			Macro:       "{$APP_PROCESSES}",
			Value:       processkeywords,
			Description: "应用进程关键字及数量，以#分隔；多个应用以|分隔，如zabbix_agentd#7|zabbix_serverd#1",
		}
		appPortMacro := zabbix.UsermacroObject{
			Macro:       "{$APP_PORT_NUMBERS}",
			Value:       portnumbers,
			Description: "应用端口号，多个端口号以|分隔，如8080|8443",
		}
		hostmacros := make(map[string]zabbix.UsermacroObject)
		hostmacros[appProcessMacro.Macro] = appProcessMacro
		hostmacros[appPortMacro.Macro] = appPortMacro
		for _, appip := range strings.Split(ips, "|") {
			his, err := zc.GetHostInterfaceFromIP(appip)
			if err != nil {
				ylog.Errorf("Zabbix", "get hostinterface for app %v error: %v", appname, err)
			}
			if len(his) == 0 {
				ylog.Infof("Zabbix", "no hostinterface found for app %v,ip :%v", appname, appip)
				continue
			}

			for _, hi := range his {
				hs, err := zc.GetHostFromHostid(hi.HostID)
				if err != nil {
					ylog.Errorf("Zabbix", "get host for app %v error: %v", appname, err)
				}
				for _, h := range hs {
					for k, v := range hostmacros {
						v.HostID = h.HostID
						margetpar := zabbix.UsermacroGetParams{}
						margetpar.HostIDs = append(margetpar.HostIDs, h.HostID)
						margetpar.Filter = make(map[string]interface{})
						margetpar.Filter["macro"] = k
						mars, err := zc.UsermacroGet(margetpar)
						if err != nil {
							ylog.Errorf("Zabbix", "get usermacro for host %v ,interfaceip :%v,error: %v", h.HostID, appip, err)
						}
						if len(mars) == 0 {
							ylog.Infof("Zabbix", "no usermacro found for host %v,interfaceip :%v", h.HostID, appip)
							ms, err := zc.HostmacroCreate([]zabbix.UsermacroObject{v})
							if err != nil {
								ylog.Errorf("Zabbix", "create  hostmacro : %v for host : %v with interfaceid : %v error : %v", k, h.HostID, appip, err)
							}
							for _, m := range ms {
								ylog.Infof("Zabbix", "hostmacro : %v created for host : %v with interfaceid : %v . id : %v", k, h.HostID, appip, m)
							}
							continue
						}

						if mars[0].Value != v.Value {
							v.HostmacroID = mars[0].HostmacroID
							ms, err := zc.HostmacroUpdate([]zabbix.UsermacroObject{v})
							if err != nil {
								ylog.Errorf("Zabbix", "update  hostmacro name: %v id: %v for host: %v with interfaceid: %v error: %v", k, v.HostmacroID, h.HostID, appip, err)
							}
							for _, m := range ms {
								ylog.Infof("Zabbix", "hostmacro name: %v id: %v updateed for host: %v with interfaceid: %v .", k, m, h.HostID, appip)
							}
						}
					}
				}
				_, err = zc.AddTempsForHosts(hs, ts)
				if err != nil {
					ylog.Errorf("Zabbix", "add temps to  host for app %v error: %v", appname, err)
				}

			}
		}
	}

}
