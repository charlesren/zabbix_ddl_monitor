通过zabbix进行网络专线通断的监控。
判断的方式：在proxy上，部署常驻的任务，使用go语言来开发，通过登录edge router， ping专线ip,来判断专线的通断。
1.可以通过zabbix api获取专线的相关信息，每个专线的信息包括专线的ip,ping的间隔,edge router设备的信息。期望能够周期性的获取专线信息。维护一个动态的专线列表。
2.edge router的信息包括router的ip、用户名、密码，router的platform等。
3.有很多专线，可能通过同一个router登录，期望能够维护edge router的连接，便于复用。
4.通过github.com/scrapli/scrapligo/库来处理到交换机的连接。支持[cisco_iosxe | cisco_iosxr | cisco_nxos |  h3c_comware | huawei_vrp ]等平台。
5.可扩展的任务系统，期望后续能够拓展的其它的任务。任务是和platform绑定的，一个任务各个平台命令不同，解析方法不同。任务command的prepare ,任务result的parse由用户定义。任务能自动注册。
6.期望任务的命令生成和结果解析能够方便的定义。
7.期望命令能够通过scrapligo的channel进行组合，或别的方式进行组合。
8.每条专线独立配置检查间隔，严格按间隔执行。
9.智能批量处理：相同路由器和间隔的专线自动合并，支持平台特定的命令批处理。
10.批量结果上报。
11.每个路由器单独调度。
