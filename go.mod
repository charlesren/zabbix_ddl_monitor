module github.com/charlesren/zabbix_ddl_monitor

go 1.24.5

replace (
	github.com/charlesren/zabbix_ddl_monitor => ./
	github.com/charlesren/zabbix_ddl_monitor/connection => ./connection
	github.com/charlesren/zabbix_ddl_monitor/internal/config => ./internal/config
	github.com/charlesren/zabbix_ddl_monitor/internal/router => ./internal/router
	github.com/charlesren/zabbix_ddl_monitor/syncer => ./syncer
)

require (
	github.com/charlesren/userconfig v0.1.0
	github.com/charlesren/ylog v0.1.0
	github.com/charlesren/zapix v0.1.1
	github.com/scrapli/scrapligo v1.3.3
	github.com/spf13/viper v1.20.1
	github.com/stretchr/testify v1.10.0
	golang.org/x/crypto v0.39.0
)

require (
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/cloudflare/circl v1.6.1 // indirect
	github.com/creack/pty v1.1.23 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fsnotify/fsnotify v1.8.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.2.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/icholy/digest v1.1.0 // indirect
	github.com/imroc/req/v3 v3.54.0 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/quic-go/qpack v0.5.1 // indirect
	github.com/quic-go/quic-go v0.53.0 // indirect
	github.com/refraction-networking/utls v1.7.3 // indirect
	github.com/sagikazarmark/locafero v0.7.0 // indirect
	github.com/sirikothe/gotextfsm v1.0.1-0.20200816110946-6aa2cfd355e4 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	github.com/spf13/afero v1.12.0 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/spf13/pflag v1.0.6 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	go.uber.org/mock v0.5.2 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/mod v0.25.0 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sync v0.15.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.26.0 // indirect
	golang.org/x/tools v0.34.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
