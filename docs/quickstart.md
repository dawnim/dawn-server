# 创建项目

需要 Go1.26+、Redis 和一种数据库。前端需要 Node20.19+ 与 pnpm。

## 1. 添加依赖

```sh
mkdir my-admin
cd my-admin
go mod init example.com/my-admin
go get github.com/dawnim/dawn-server
mkdir -p bootstrap config app/admin app/providers database/migrations storage/app/private
```

## 2. 注册服务

在 `bootstrap/app.go` 创建应用：

```go
package bootstrap

import (
    dawn "github.com/dawnim/dawn-server"
    "github.com/prismgo/framework/encryption"
    "github.com/prismgo/framework/foundation"

    "example.com/my-admin/app/admin"
    "example.com/my-admin/app/providers"
    _ "example.com/my-admin/config"
    _ "example.com/my-admin/database/migrations"
)

func NewApplication() *foundation.Application {
    return foundation.Configure().
        WithExtensionProviders(encryption.ServiceProvider{},
            dawn.ServiceProvider{Configure: admin.Configure}).
        WithProviders(providers.AppServiceProvider{}).
        WithRouting(func(r *foundation.Routing) {
            r.MigrationPaths("database/migrations")
            r.Routes(dawn.RegisterRoutes)
        }).Create()
}

```

`main.go` 只处理命令入口：

```go
package main

import (
    "context"
    "os"
    dawn "github.com/dawnim/dawn-server"
    "example.com/my-admin/bootstrap"
    "github.com/prismgo/framework/console"
)

func main() {
    if err := dawn.HandleCommand(context.Background(), bootstrap.NewApplication(), os.Args); err != nil {
        console.Exit(err.Error())
    }
}
```

## 3. 配置和菜单

在 `config/*.go` 的 `init` 中调用 `config.Add` 注册命名空间；[配置说明](configuration.md) 给出了必需配置。应用名与环境共同组成缓存命名空间，同一部署的各实例保持一致。

`app/admin/menu.go` 定义项目自己的后台入口：

```go
package admin

import sdk "github.com/dawnim/dawn-server/app/admin"

func Configure(o *sdk.Options) {
    // 在此追加项目菜单、权限、缓存定义和路由。
}
```

队列监控已内置在“运维管理”中，不需要另外挂载页面。项目仍通过config/horizon.go配置串行组和并发组。

## 4. 初始化数据库

创建 `database/migrations/202610310000_create_admin_tables.go`：

```go
package migrations

import (
    "fmt"
    adminmigrations "github.com/dawnim/dawn-server/database/migrations"
    "github.com/prismgo/framework/database"
    "gorm.io/gorm"
)

func init() {
    database.RegisterMigration(func(db *gorm.DB) error { return adminmigrations.Install(db) }, func(*gorm.DB) error {
        return fmt.Errorf("系统表通过备份恢复")
    })
}
```

迁移的up函数定义在当前文件中，注册名会取该文件名；不要直接传入其他文件里的函数。

创建 `.env`，设置 `DB_CONNECTION` 和数据库连接参数、`REDIS_URL`。MySQL支持分项配置；PostgreSQL使用 `DB_DSN`，SQLite使用 `DB_DATABASE`。生成32字节密钥，将完整输出写入 `APP_KEY`：

```sh
printf 'base64:'; openssl rand -base64 32
go run . dawn:install
```

命令执行全部系统和项目迁移，创建管理员、Dawn部门、CEO岗位、附件分类和默认配置，并下载本地IP地理库。默认管理员为`admin`，密码为`123456`；需要自定义时设置`DAWN_ADMIN_PASSWORD`。重复执行保留已有密码和配置。

后续更新系统表使用 `go run . dawn:migrate`。迁移由发布流程执行，请求处理和常规启动不改表。

## 5. 运行

```sh
go run . dawn:start all
```

该命令分别守护HTTP、WS、Horizon、cron；某个服务退出时只重启该服务。Ctrl+C或SIGTERM停止全部服务。

生产构建：`go build -o bin/server .`，运行 `./bin/server dawn:start all`。前端静态文件和 `/admin/`、`/ws` 代理由 Nginx 配置。

本地前端放在 `admin/`，提供 `pnpm run dev` 脚本，安装依赖后运行 `go run . dawn:start local`。前端默认访问 `http://127.0.0.1:5173`。单服务控制、独立部署和平滑重载见[服务运行](runtime.md)。同一部署只保留一个调度实例和一份串行队列消费者。
