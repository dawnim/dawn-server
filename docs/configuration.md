# 配置

配置分成启动参数和运行参数。数据库、端口、密钥等启动参数放在 `config/*.go`，从环境变量读取；后台可编辑的业务参数注册到 `Options.Settings`，值保存在 `admin_settings`的分组JSON中。

## 应用与基础服务

在配置包的 `init` 中注册命名空间；启动文件空导入该包。

```go
package config

import cfg "github.com/prismgo/framework/config"

func init() {
    cfg.Add("app", func() map[string]any {
        return map[string]any{"name": cfg.Env("APP_NAME", "my-admin"),
            "env": cfg.Env("APP_ENV", "local"), "key": cfg.Env("APP_KEY", ""),
            "server": map[string]any{"host": cfg.Env("SERVER_HOST", ""), "port": cfg.Env("SERVER_PORT", 5123)}}
    })
    cfg.Add("logging", func() map[string]any {
        return map[string]any{"default": cfg.Env("LOG_CHANNEL", "single"), "channels": map[string]any{
            "single": map[string]any{"driver": "single", "level": cfg.Env("APP_LOGGER_LEVEL", "info"),
                "path": cfg.Env("APP_LOGGER_FILE", "storage/logs/dawn.log"), "formatter": "line"}}}
    })
    cfg.Add("database", func() map[string]any {
        return map[string]any{
            "default": cfg.Env("DB_CONNECTION", "mysql"),
            "connections": map[string]any{
                "sqlite": map[string]any{
                    "driver": "sqlite", "database": cfg.Env("DB_DATABASE", "storage/database.sqlite"),
                    "max_open_conns": 1, "max_idle_conns": 1,
                },
                "mysql": map[string]any{
                    "driver": "mysql", "host": cfg.Env("DB_HOST", "127.0.0.1"),
                    "port": cfg.Env("DB_PORT", 3306), "database": cfg.Env("DB_DATABASE", "my_admin"),
                    "username": cfg.Env("DB_USERNAME", "root"), "password": cfg.Env("DB_PASSWORD", ""),
                    "charset": "utf8mb4", "parse_time": true, "loc": "UTC", "strict": false,
                    "dsn": cfg.Env("DB_DSN", ""), "max_open_conns": 30, "max_idle_conns": 10,
                    "conn_max_lifetime": "1h", "conn_max_idle_time": "10m",
                },
                "pgsql": map[string]any{
                    "driver": "pgsql", "dsn": cfg.Env("DB_DSN", "host=127.0.0.1 port=5432 user=postgres dbname=my_admin sslmode=disable"),
                    "max_open_conns": 30, "max_idle_conns": 10,
                    "conn_max_lifetime": "1h", "conn_max_idle_time": "10m",
                },
            },
            "redis": map[string]any{
                "client": "go", "options": map[string]any{},
                "default": map[string]any{"url": cfg.Env("REDIS_URL", "redis://127.0.0.1:6379/0")},
                "cache": map[string]any{"url": cfg.Env("REDIS_CACHE_URL", "redis://127.0.0.1:6379/1")},
            },
        }
    })
    cfg.Add("filesystem", func() map[string]any {
        return map[string]any{"default": "local", "disks": map[string]any{
            "local": map[string]any{"driver": "local", "root": "storage/app/private", "visibility": "private"},
        }}
    })
    cfg.Add("cache", func() map[string]any {
        return map[string]any{"default": "redis", "prefix": "my-admin:local",
            "stores": map[string]any{"redis": map[string]any{"driver": "redis", "connection": "default"}}}
    })
    cfg.Add("queue", func() map[string]any {
        return map[string]any{"default": "redis", "connections": map[string]any{
            "redis": map[string]any{"driver": "redis", "connection": "default", "prefix": "my-admin:local:queue", "retry_after": 90},
        }, "failed": map[string]any{"driver": "redis", "store": "default", "prefix": "my-admin:local:failed"}}
    })
}
```

## 数据库连接

| 连接类型 | DSN示例 |
|---|---|
| sqlite | `storage/database.sqlite?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)` |
| mysql / mariadb | `user:password@tcp(127.0.0.1:3306)/database?charset=utf8mb4&parseTime=true&loc=UTC` |
| pgsql | `host=127.0.0.1 port=5432 user=postgres password=password dbname=database sslmode=disable` |

`database.default` 选择连接名称；每个连接单独定义在 `database.connections`，由 `driver` 指定驱动。MySQL 使用 `DB_HOST`、`DB_PORT`、`DB_DATABASE`、`DB_USERNAME`、`DB_PASSWORD`，也可用 `DB_DSN` 覆盖连接串。MariaDB 复用 `mysql` 驱动和这些选项；PostgreSQL 扩展驱动通过 `DB_DSN` 配置。

SQLite 使用 `DB_DATABASE` 指定文件，父目录须存在；连接池默认1个连接。连接池支持 `max_open_conns`、`max_idle_conns`、`conn_max_lifetime`、`conn_max_idle_time`。SSL与驱动选项保留在对应连接的 `ssl` 和 `options` 中。

Redis连接支持：

```text
无密码：redis://127.0.0.1:6379/0
有密码：redis://:password@127.0.0.1:6379/0
ACL：redis://username:password@127.0.0.1:6379/0
TLS无认证：rediss://cache.example.com:6379/0
TLS有密码：rediss://:password@cache.example.com:6379/0
TLS与ACL：rediss://default:password@primary.example.cache.amazonaws.com:6379/0
```

密码中的特殊字符按URL编码，例如 `@` 写成 `%40`。URL与分项配置择一使用；分项地址、认证和数据库字段非空时会覆盖URL。分项配置使用 `REDIS_HOST`、`REDIS_PORT`、`REDIS_USERNAME`、`REDIS_PASSWORD`、`REDIS_MAIN_DB`。

缓存、队列、会话和Horizon通过连接名复用 `database.redis`，各组件中不重复声明连接地址。缓存默认使用 `default`；需要独立缓存连接时，设置 `CACHE_REDIS_CONNECTION=cache` 并配置 `REDIS_CACHE_URL`。

`rediss://` 启用TLS并校验证书，使用服务提供的DNS端点。设置到 `REDIS_URL`，独立缓存连接使用 `REDIS_CACHE_URL`。[Redis连接URL说明](https://redis.io/docs/latest/develop/clients/connection-urls/)

AWS Serverless使用TLS，节点型实例按传输加密设置连接。AWS示例替换为实际主端点和账号；当前配置适用于单节点或关闭Cluster Mode的实例。[AWS说明](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/in-transit-encryption.html)

## 后台与跨域

注册 `dawn` 命名空间：

```go
cfg.Add("dawn", func() map[string]any {
    return map[string]any{"module": "example.com/my-admin", "admin_prefix": "/admin",
        "ws_addr": ":8851", "ws_path": "/",
        "allowed_origins": cfg.Env("ALLOWED_ORIGINS", "*")}
})
```

HTTP监听地址统一使用 `app.server.host/port`，环境变量为 `SERVER_HOST`、`SERVER_PORT`，默认端口5123。WS使用 `WS_ADDR`、`WS_PATH`，默认监听8851。

代理、请求超时和关闭等待统一由 `app.server` 管理。`SERVER_TRUSTED_PROXIES` 用逗号分隔，例如 `127.0.0.1,10.0.0.0/8`；`SERVER_SHUTDOWN_TIMEOUT` 设置关闭等待，例如 `15s`。

`ALLOWED_ORIGINS=*` 允许任意来源；域名白名单用空格分隔，例如 `https://admin.example.com https://ops.example.com`。配置同时用于HTTP与WS。

任意来源模式使用Bearer认证，跨域请求不携带Cookie；同源iframe和下载继续使用登录Cookie。白名单模式允许配置中的来源携带凭证。跨域策略只决定浏览器能否读取响应，接口仍校验登录和权限。

## 队列与监控

注册 `horizon` 配置，串行和并发任务分组：

```go
cfg.Add("horizon", func() map[string]any {
    return map[string]any{"path": "/admin/horizon", "use": "redis", "prefix": "my-admin:local:horizon:",
        "environments": map[string]any{"*": map[string]any{
            "supervisor": map[string]any{"connection": "redis", "queue": []string{"default"},
                "balance": "false", "min_processes": 1, "max_processes": 1, "tries": 3, "timeout": 60},
            "multiples": map[string]any{"connection": "redis", "queue": []string{"exports"},
                "balance": "auto", "min_processes": 1, "max_processes": 4, "tries": 3, "timeout": 60},
        }}}
})
```

`supervisor` 同一部署只运行一份；并发扩容使用 `multiples`。任务名称、队列与payload编码在HTTP和worker进程中保持一致。

## 语言

固定提示以中文原文为key，英文包提供译文，缺译文时仍按key填入参数。HTTP按 `Accept-Language` 返回提示，CLI使用 `APP_LOCALE`；WS沿用签发连接票据时的语言。动态字段按模块维护zh/en映射，接口字段名保持不变。

## 密钥、附件与缓存

`APP_KEY` 为 `base64:` 加32字节随机值。保密配置加密保存，读取使用 `admin.Resolve().Setting(ctx, key)`。更换密钥前完成旧数据转换，并保留所需旧密钥。

附件默认使用私有磁盘。前端先计算MD5和SHA-256并预检，上传后由服务端再次校验。多主机使用共享磁盘或对象存储；下载保留权限和文件归属检查。

缓存模板统一定义在 `constants/key.go`，业务用同一 `Definition` 类型注册到 `Options.CacheKeys`。`App.Key` 加应用与环境前缀。锁、永久缓存、临时缓存分别使用Lock、Forever、Temp命名，临时值声明TTL，清理能力单独授权。

## 日志与配置作用范围

日志目录为 `storage/logs/`，应用日志默认 `dawn.log`。使用daily通道时按日期生成 `dawn-YYYY-MM-DD.log`；错误通道独立写入 `storage/logs/error/error-YYYY-MM-DD.log`。环境变量沿用 `LOG_CHANNEL`、`APP_LOGGER_FILE`、`APP_LOGGER_LEVEL` 等名称。

`FILESYSTEM_DISK`决定附件默认磁盘，`ADMIN_DISK`可单独覆盖。`SESSION_*`配置框架Session；管理员Token有效期使用 `DAWN_ACCESS_TTL`、`DAWN_REFRESH_TTL`，默认均为30天，使用中临近过期自动轮转，刷新后续期30天。队列批次、失败记录和重启通知使用共享Redis，支持独立worker进程。

基础配置保留app、database、cache、session、queue、filesystem、logging、encoding的命名空间及字段层级。Dawn运行参数放 `config/dawn.go`；`.env.example`列出全部基础配置，高级选项按需取消注释。

## 系统配置

个人中心为仪表盘下的代码内置隐藏路由，不需要数据库菜单记录。`PUT /admin/auth/profile`只更新传入的资料字段，单独传`avatar`不会覆盖其他资料；传空字符串可清空邮箱、手机等可选字段。`GET /admin/auth/me`返回所属部门的`id`和`name`，个人中心日志通过`self=1`限制为当前账号。队列监控接口字段统一使用小写snake_case，前端字段语言包使用相同标识。

后台的站点、上传、邮件参数按组展示。数据库每组一行，key是稳定标识，name是中文配置名称，value保存JSON。值类型、表单控件、选项、排序和说明留在代码定义中；`dawn:install`补齐分组和新增字段，并保留已保存的值。业务配置通过`Options.Settings`声明，使用`App.Setting(ctx, key)`读取。

站点名称、Logo、版权、简介、关键词和备案号用于前端展示，`GET /admin/site`公开这些展示字段及程序时区、服务器时间。选为站点Logo的图片可由未登录用户读取，其他附件继续检查登录和归属。保存站点配置后界面立即更新，刷新页面也会重新加载。

密钥使用应用密钥加密保存，接口仅返回掩码；留空表示保留原值。修改密钥前备份`APP_KEY`，多实例使用同一应用密钥。

上传模式包含本地、阿里云OSS、七牛云、腾讯云COS、Amazon S3。选择模式后填写对应的Bucket、Endpoint、区域和凭证。本地路径留空时使用文件系统中的本地磁盘。七牛与COS使用各自的S3兼容端点；S3支持Path Style和自定义端点。云端Bucket由运维提前创建。

附件记录上传时的驱动、Bucket和路径，密钥从对应驱动的保密配置读取。切换上传模式不会移动旧文件；轮换凭证时须保留对旧Bucket的读取权限。管理端下载仍通过授权接口读取，Bucket可保持私有。上传前检查大小和扩展名，MD5与SHA-256用于秒传，服务端重新计算摘要。

SMTP支持STARTTLS与SSL/TLS，默认UTF-8编码。测试邮件使用当前配置发送，发送结果记录到邮件日志。

## IP地理位置

`dawn:install`下载GeoLite2 City到`storage/app/geoip/GeoLite2-City.mmdb`，登录日志保存国家代码与地理名称，内网地址单独标识。查询在本地完成，不向外发送客户端IP。

`dawn:start all/local`包含cron，每日03:30更新；也可运行`go run . dawn:geoip`。新文件下载并校验后原子替换，失败保留现有库，HTTP进程自动重载。多主机各使用本地库时，每台主机安排自己的更新任务。

`GEOIP_PATH`指定本地路径，`GEOIP_URL`可指定自己的HTTPS镜像，`GEOIP_ENABLED=false`关闭地理库下载和查询。默认下载源和数据许可见README鸣谢。

## 缓存管理

缓存定义的`Readable`、`Editable`分别控制查看和编辑，`Clearable`只控制是否纳入顶栏“清理缓存”。页面单项删除不受`Clearable`限制，需要`dawn.cache.delete`权限，且Key必须属于当前应用、环境和所选的已注册定义。编辑还需`dawn.cache.update`权限，支持string值和hash单字段，保留原TTL；hash查看按游标分页，不用部分内容覆盖整个hash。配置缓存通过配置页面修改，验证码、会话、锁等关键Key默认不开放查看、编辑，也不纳入批量清理，但可手动删除。

顶栏“清理缓存”只扫描当前应用与环境下标记为`Clearable`的Key。配置每组使用独立Redis Hash，例如`forever:admin:settings:site`、`forever:admin:settings:upload`。Hash中的每个Field对应组内一个参数，JSON编码保留值类型，`__revision`为内部版本字段。保存时原子更新该组，防止多个实例将旧值写回。

## 操作日志

写操作记录请求参数和JSON响应，正文上限16KiB。参数按原值记录；认证、管理员写入、配置、上传、缓存值接口不记录正文，不采集Authorization与Cookie。响应记录用于区分同一HTTP状态下的业务结果，不记录文件内容或流式响应。

## 代码生成

“引用模式”生成字段、默认值、独立API和页面入口，通过`CrudPage`使用内置标准页面；“源码模式”将同一套列表、搜索栏、编辑弹窗源码生成到项目中，可直接修改。两者初始UI与交互一致，使用原版表格工具栏、操作按钮、分页排序与批量删除。两种模式都生成zh/en字段语言包，以接口字段名为映射key，由页面自动加载。中文显示名取字段注释，英文按字段名生成；固定提示使用中文原文key，缺少译文时仍渲染占位符。可勾选“生成表迁移”，输出独立的建表快照，保留字段类型、默认值、普通及联合唯一索引与注释。已有表跳过建表，后续字段变更通过新增迁移处理。下载和生成到项目都会先打开编辑弹窗，可逐文件修改内容；“生成表迁移”在弹窗顶部勾选，切换时保留其他文件的编辑。编辑后的内容按弹窗原文下载或写入，文件路径限于本次生成范围。生成到项目仅允许本地环境的超级管理员执行，使用当前项目根目录，已有文件不覆盖。

生成后在项目菜单和路由中注册入口，再检查字段类型、校验规则与业务数据范围。含`created_by`的表自动加入归属范围，更新时不允许修改归属字段。复杂查询、金额精度与关联数据按业务调整。

## 服务监控

监控展示处理请求的主机与当前进程。负载圆盘按一分钟负载除以逻辑CPU核数计算，内存和磁盘展示实际已用容量。`DAWN_MONITOR_DISK_PATH`指定磁盘路径，留空使用项目所在磁盘。容器中展示进程可见的资源；不支持的指标显示为空。

监控和队列页面自动请求最新数据，后台更新不显示加载遮罩，失败保留上次结果。离开页面或隐藏浏览器标签时暂停请求。
