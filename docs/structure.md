# 目录与扩展

## 服务端目录

```text
provider.go                         # 对外服务注册和路由入口
app/
  admin/
    app.go options.go               # 后台上下文和扩展配置
    auth.go menu.go settings.go     # 共享授权、菜单定义和参数读取
    controllers/                    # 各后台功能的HTTP处理
    middleware/                     # 登录、权限、跨域、审计
    response/                       # 统一响应、分页与主键解析
  models/                           # 每个实体一个文件
  providers/                        # 容器绑定和启动注册
  cmd/                              # 服务启动、迁移、初始管理员
database/migrations/                # 系统表及升级
routes/admin.go                    # 后台路由装配
constants/key.go                   # 缓存定义
realtime/                          # WS消息与连接恢复
```

控制器处理HTTP输入、调用业务能力并返回响应；中间件在入口统一处理登录、权限、跨域和审计。共享授权与配置读取不依赖具体请求，因此CLI、队列和业务接口可以共用。服务提供者只绑定资源，实际使用时再建立连接。

模型按实体分文件，`base.go`保存共用主键和毫秒时间字段；角色的数据范围定义与角色模型放在一起。

## 业务项目

后台专属内容放在 `app/admin`：菜单和权限放 `menu.go`，路由放 `routes.go`，处理函数放 `controllers`，按 `user.go`、`order.go` 等功能命名。共享模型放 `app/models`，共享定义放 `app/constants`。事件、监听器、队列和调度分别放 `app/events`、`app/listeners`、`app/jobs`、`app/schedule`。

小功能可以直接写在控制器中。数据库事务或处理逻辑确实被多个入口调用时，再放入 `app/services`。

## 注册CRUD

```go
import (
    sdk "github.com/dawnim/dawn-server/app/admin"
    crud "github.com/dawnim/dawn-server/app/admin/controllers"
    "github.com/gin-gonic/gin"
    "gorm.io/gorm"
)

func Register(a *sdk.App, g *gin.RouterGroup) {
    crud.RegisterResource(a, g, crud.Resource[Record]{
        Path: "/records", Permission: "app.records",
        Fields: []string{"title", "created_by", "department_id"}, Search: []string{"title"},
        Scope: func(db *gorm.DB, current *sdk.Actor) *gorm.DB {
            return db.Scopes(current.Scope("created_by", "department_id"))
        },
        Prepare: func(row *Record, current *sdk.Actor, editing bool) {
            if !editing { row.CreatedBy = current.User.ID; row.DepartmentID = current.User.DepartmentID }
        },
    })
}
```

Fields是可写字段白名单。Scope同时作用于列表、详情、修改、删除和导出；Prepare写入服务端确定的归属字段。复杂流程直接注册独立处理函数。

## 菜单与权限

代码定义系统页面和业务页面，数据库保存角色授权和自定义菜单。`dawn.*` 是稳定的系统权限协议，`app.*` 用于业务，`custom.*` 用于自定义菜单和操作权限。菜单显隐和接口检查使用同一个已登记的权限key。

自定义菜单按type区分：1目录、2菜单、3按钮、4外链。目录可包含目录和菜单，按钮挂在菜单下，仅参与授权。页面菜单指定路由与组件路径，外链指定站内路径或HTTP(S)地址。创建时校验唯一key，保存后保持不变。普通外部网页的权限由目标系统负责；受控系统可用一次性票据交换身份，再按实时授权保护自己的API。

## 迁移和升级

系统迁移只在显式命令中执行，创建扩展内置表并登记版本。常规启动与请求处理不执行DDL。

业务迁移使用唯一时间前缀，在启动包中空导入并登记目录。部署保留与二进制同版的迁移文件，发布流程只安排一个实例执行DDL。

## 组合运行

`dawn:start all` 使用独立子进程运行各服务；主进程持有本地控制通道和监听资源。服务异常退出时单独重试，主动停止后保持停止。HTTP/WS重载通过监听器继承交接，新进程未就绪时保留旧进程。Horizon继续管理自己的worker，Dawn通过公开扩展接口限定本实例的停机范围。

命令位于 `app/cmd`，进程状态与回收放在 `support/supervisor`。每个进程拥有自己的Application和连接池，共享状态放数据库、Redis。操作见[服务运行](runtime.md)。
