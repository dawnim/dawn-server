# 服务端开发规范

## 目录

| 路径 | 职责 |
|---|---|
| provider.go | 公开的服务注册与路由入口 |
| app/admin | 后台上下文、授权、菜单、参数读取 |
| app/admin/controllers | 各功能HTTP处理与通用CRUD |
| app/admin/middleware | 登录、权限、CORS和审计 |
| app/admin/response | 共用响应、分页与主键解析 |
| app/models | 每个实体一个文件 |
| app/providers、app/cmd | 容器装配与命令 |
| database/migrations | 扩展内置表结构与版本登记 |
| routes、constants、realtime | 路由装配、Key定义、实时通信 |

控制器处理请求，非HTTP共用能力留在后台上下文。优先在已有职责下修改代码，避免新建重复入口。每个进程拥有唯一Application和Console Kernel。

管理员聊天的模型、表结构、接口、消息投递与运行装配由dawn-server完整提供，表和索引纳入统一安装迁移；前端由dawn-admin提供。业务工程只注册、配置和安装扩展，业务实时处理器独立于内置聊天。

## 编码

- 优先标准库和已有运行时能力。直接实现简单逻辑；确有多处复用再提取函数。业务层按需要增加，不为形式创建Service/Repository。
- Go文件小写加下划线，一个实体一个文件；控制器按功能命名。共用字段放base.go，固定选项贴近实体。
- Register只绑定懒加载factory，Boot注册驱动、命令和监听器。连接由容器统一创建和释放，业务入口不另建一套资源。
- 实体业务字段用gorm comment说明用途，类型、状态写出取值含义；迁移为内置表及关联表设置表注释。常规主键和创建、更新、删除时间不重复注释。
- 时间统一int64 Unix毫秒，软删除0表示有效；建表显式使用整数时间列。结构与调用方同步修改，不写历史兼容代码。
- 管理接口默认/admin/。服务端检查权限与数据Scope；列表、详情、写入、导出、下载使用一致的数据范围，归属字段由服务端决定。
- dawn.*、app.*、custom.*是稳定的系统、业务、自定义授权key；保存后不改名、不复用。
- 缓存集中定义，锁以Lock开头，永久缓存以Forever开头，临时缓存以Temp开头；声明TTL及Readable/Editable/Clearable策略，关键基础设施Key不开放编辑或清理。
- Event在事务提交后派发；Job注册类型、传递context并保证重试可接受。共享状态存数据库/Redis，goroutine有明确退出路径。
- 配置通过Add/Env注册，保留组件的命名空间、字段和层级；SQL与Redis连接放database，其他组件引用连接名。HTTP监听、代理和超时统一使用app.server，不在后台路由中重复覆盖。环境变量可覆盖默认值。密码、token和密钥不写日志；保密业务参数加密保存。
- HTTP跨域默认支持*，Bearer请求不携带跨域Cookie；凭证模式使用明确来源列表。WS按同一来源配置校验，握手后仍需认证。
- 修改Go文件后运行gofmt/goimports；公开扩展契约有简短说明，测试验证实际行为和权限边界。

## 变更与验证

保留其他任务的修改，确认当前Git根目录后再操作。依赖问题改源码或版本配置，不修改module cache和node_modules。

外部数据库测试只使用明确指定的专用空库。没有连接变量时跳过外部矩阵测试。只清理本次创建并确认的进程，不重启既有服务。

运行与改动相符的测试，分别报告代码检查、服务联调和界面验收。提交、推送、发布和部署按用户指令执行。

## 检查

```sh
go test -race ./...
go vet ./...
DAWN_TEST_DRIVER=pgsql DAWN_TEST_DSN='专用DSN' go test -run 'Test(Application|DatabaseMatrix)$' -count=1 ./tests
```

表名调整必须同时检查关联表、Scope中的显式表名、索引与迁移记录。不通过运行时DDL或读取时转换来修复历史结构。修改CORS时覆盖预检、白名单和任意来源；修改组合运行时覆盖取消、端口失败、服务退出与worker收尾。

## 文档

若修改 README.md 则dawn-server、dawn-admin、dawn-simple下的README.md同步更新为同样内容

全部教程集中维护在dawn-simple/docs，修改公开接口时同步教程。

文档面向使用者，直接说明已实现功能、核心逻辑和用法。文字简短准确，示例使用通用业务名称；复杂流程使用GitHub支持的Mermaid基础流程图或时序图，并验证语法。文档内部使用相对链接，跨仓库和源码链接使用GitHub地址。第三方来源集中到README鸣谢，许可证保留版权声明。

界面和接口提示使用语言key；按请求读取locale，禁止用全局SetLocale处理并发请求。

## 服务管理

启动使用 `dawn:start all`，本地开发前后端使用 `dawn:start local`。服务列表用逗号分隔，操作为start、stop、restart、reload、status；stop保持停止，reload仅用于HTTP/WS。每个服务独立进程，管理器只恢复期望运行的服务，不重复接管Horizon内部worker。

复用框架服务生命周期与监听器继承，重载必须等待新进程就绪；失败时保留旧进程。禁止提前取消在途HTTP请求或把WS连接直接迁移到新进程。验证异常拉起、主动停止、交接失败、端口占用、父进程退出和子孙进程回收。

配置直接使用标准字段和类型，不重复验证静态配置。应用日志默认storage/logs/dawn.log；新增配置同步.env.example，不删基础配置键。

系统配置按组一行保存JSON，中文名称落库，表单元数据留在代码定义；初始化只补缺失字段，不覆盖用户配置值。缓存按组使用独立Hash，每个Field对应组内参数，保留__revision版本防止旧值回写。保密配置加密保存，不写入附件位置快照。新增存储驱动复用统一文件系统接口；校验切换驱动后旧附件仍可读取。

代码生成的引用模式和源码模式保留相同的标准列表交互，均输出独立API与zh/en字段语言包。迁移按勾选生成独立结构快照；后续字段变化增加新的迁移，不修改已发布迁移。生成文件需独立编译，迁移在专用空库执行验证。

## 语言与文案

固定界面、错误和通知文案通过翻译函数使用中文原文key。中文直接回退原文，zh语言包只保存fields等技术标识的中文显示映射；英文及其他语言包以中文原文key定义译文。接口字段名保持原样。缺少译文时使用key并渲染占位符；修改中文措辞时同步调用处、key和各语言译文。中文尽量不加中间空格，句末不加标点；命令、路径、代码保留必要空格。

动态内容通过占位符传入，禁止拼接后翻译。前端使用命名占位符，例如`t('您于{v0}执行了{v1}', { v0: time, v1: action })`；后端保留格式占位符并按请求locale翻译。各语言占位符名称、数量和类型保持一致，翻译可调整命名参数顺序。业务语言包按模块组织，生成页面自动加载本模块语言包，通用文案使用公共语言包。

## 命名

Go导出标识使用PascalCase，缩写保留ID、IP、HTTP、URL等标准大写；Go文件使用snake_case。前端变量和函数使用camelCase，组件名使用PascalCase。数据库列、HTTP/WS请求与响应字段统一使用小写snake_case，例如id、user_id、created_at；界面中的ID、IP、PID仍使用标准大写。第三方结构在接口边界适配，不向调用方暴露Go字段名，也不保留大小写两套字段或兼容别名；框架原生配置与公开组件参数遵循各自契约。

字段语言包固定使用fields.<模块>.<接口字段名>，zh/en使用相同层级和相同snake_case字段，与接口逐字一致。禁止同时定义ID/id、Host/host、JobName/job_name等同义映射；新增或改名时同步接口、页面、生成模板及语言包。菜单和权限的稳定key不因风格调整改名。

## REST接口

HTTP接口以复数资源和方法表达操作：GET集合/详情，POST新增，PATCH局部修改，DELETE删除；PUT完整替换资源或密码、权限等子资源；普通CRUD同时支持PATCH和PUT。路径小写，多词用连字符；JSON字段与查询参数使用snake_case。所有资源直接位于/admin下，不添加system/safeguard/tool/business等技术分组；层级只表达真实资源关系。权限key保持稳定。

批量删除使用DELETE集合?ids=1,2，一次请求、一份事务；必须提供明确ID，不允许整集合删除。写入只接受公开字段，主键、审计与归属字段由服务端维护。PATCH未提交的字段保持原值；PUT必须提供必填字段，省略的可选字段恢复模型默认值或零值。两者均正常写入0、false、空字符串；null只允许可空字段。

列表参数为page、page_size、sort_by、sort_order，默认1/20，不设置每页条数上限。排序使用白名单并追加主键保证稳定；计数、列表、详情、写入和导出使用一致的数据范围。普通列表返回{data:[],meta:{page,page_size,total}}，树返回{data:[]}，Redis使用明确游标。禁止前端伪造总数、固定第一页或静默截断数据。

成功使用HTTP状态：查询/修改200，新增201并设置Location，删除204无正文；详情/新增/修改返回{data:...}，错误返回{message,errors?}。前端只在统一request层解包，不能依赖业务code或兼容多套响应。分页越界需重新取有效页的数据。

CSV导出是GET集合的text/csv表示，沿用当前筛选但忽略分页；导入使用POST集合/imports和multipart的file字段，模板为GET集合/import-template。导入事务验证全部行，不忽略错误；文件响应不套JSON。

前端模块API统一list/get/create/update/replace/remove/removeMany，update(id,input)使用PATCH，replace(id,input)使用PUT，组件通过API调用。生成器的引用模式与源码模式遵循同一协议、字段、权限、导入导出和校验规则；同时维护公开类型、模板与文档。源代码和安装包都需编译与实际CRUD验证。
