# 服务运行

## 启动

在项目根目录执行：

```sh
go run . dawn:start all
go run . dawn:start http,ws
go run . dawn:start cron
go run . dawn:start local
```

不传参数时默认为all，运行HTTP、WS、Horizon和cron。local增加项目的前端开发服务器，默认目录admin、端口5173。前端提供pnpm run dev脚本；首次在admin目录执行pnpm install。数据库、Redis和初始化迁移在启动前准备好。

HTTP默认5123，WS默认8851。各服务独立进程，异常退出时按1、2、4秒逐步延迟重试，最大30秒。稳定运行一分钟后恢复初始间隔。主进程退出时统一等待服务收尾。

## 在另一终端控制

```sh
go run . dawn:start all status
go run . dawn:start horizon restart
go run . dawn:start http reload
go run . dawn:start ws reload
go run . dawn:start cron stop
go run . dawn:start cron
go run . dawn:start all stop
```

start启动或恢复守护；stop保持停止；restart先停止再启动；reload先启动新进程再交接，仅用于HTTP和WS。all status显示各服务的状态、PID、自动重启次数、启动时间和最近错误；all stop包括local启动的前端，并退出主进程。

相同部署目录的命令通过本地socket连接现有管理器，重复执行start不会创建另一组服务。控制仅作用于本机本项目，不广播到其他实例。

Horizon内部仍由原生master、supervisor和worker管理。单实例停机等待任务收尾，不修改整个部署的terminate标记。异常退出后的重建遵守原生租约，租约到期前保持退避；通过status和日志查看进度。新进程启动失败会返回错误，管理器保留运行目标继续重试。

## 平滑重载

HTTP监听socket由主进程持有。reload让新进程继承监听资源，收到就绪通知后才停止旧进程接收新请求，并等待在途请求结束。新版本启动失败时保留旧进程。等待时间沿用SERVER_SHUTDOWN_TIMEOUT。

WS新进程接收新连接，旧进程分散发送1012关闭通知；客户端重连并通过Redis恢复逻辑会话、订阅和消息确认位置。已建立的连接不跨进程转移；超过排空时间后关闭剩余连接。消息恢复受Redis保留时间和消息条数限制。

进程守护和监听器交接面向Linux、macOS。生产构建使用固定的二进制路径；替换二进制后执行reload或restart。go run使用临时二进制，修改Go源码后重新运行命令。修改监听地址需要重新启动管理器。

## 本地前端

config/dawn.go注册以下配置：

```go
"local": map[string]any{
    "directory": Env("DAWN_FRONTEND_DIRECTORY", "admin"),
    "port": Env("DAWN_FRONTEND_PORT", 5173),
},
```

启动器把解析后的HTTP地址、WS地址及路径传给前端子进程。Vite可使用：

```ts
const adminPrefix = process.env.DAWN_ADMIN_PREFIX || '/admin'

export default defineConfig({
  plugins: [vue()],
  define: { 'import.meta.env.VITE_ADMIN_PREFIX': JSON.stringify(adminPrefix) },
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: {
      [adminPrefix]: process.env.DAWN_HTTP_TARGET || 'http://127.0.0.1:5123',
      '/api': process.env.DAWN_HTTP_TARGET || 'http://127.0.0.1:5123',
      '/ws': {
        target: process.env.DAWN_WS_TARGET || 'ws://127.0.0.1:8851',
        ws: true,
        rewrite: () => process.env.DAWN_WS_PATH || '/'
      }
    }
  }
})
```

createAdmin的apiBase使用import.meta.env.VITE_ADMIN_PREFIX。浏览器访问前端端口，HTTP与WS均由Vite代理。端口占用时直接报错，不自动换端口。

## 生产托管与日志

```sh
go build -o bin/server .
./bin/server dawn:start all
```

systemd、Supervisor或容器重启策略只托管这一条主进程。前端构建产物由Nginx提供，生产使用all。

独立托管同一目录下的服务时，各管理器使用不同的本地控制目录：

```sh
DAWN_RUNTIME_DIRECTORY=storage/framework/http ./bin/server dawn:start http
DAWN_RUNTIME_DIRECTORY=storage/framework/ws ./bin/server dawn:start ws
DAWN_RUNTIME_DIRECTORY=storage/framework/http ./bin/server dawn:start http reload
```

配置键为dawn.runtime_directory，默认storage/framework。不要同时对相同服务启用组合托管和独立托管。cron和串行队列消费者同一部署保留一份。

应用日志默认storage/logs/dawn.log，APP_LOGGER_FILE修改路径，LOG_CHANNEL选择single、daily、stdout等通道。daily生成dawn-YYYY-MM-DD.log；默认错误通道按日写入storage/logs/error/。终端输出由外部进程管理器独立收集。
