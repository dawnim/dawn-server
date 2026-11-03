# 后台开发约定

- 注册dawn.ServiceProvider，调用dawn.RegisterRoutes，复用同一个Application和资源容器。
- app/admin管理后台上下文；controllers处理HTTP，middleware处理入口策略；模型逐个放app/models。
- 缓存模板使用admin命名空间，时间字段int64 Unix毫秒。
- 服务端校验权限和Scope，菜单与权限key保持稳定；归属字段由服务端写入。
- 配置放config/*.go，跨域*使用Bearer，白名单可支持凭证。
- 事务提交后派发事件，Job注册类型并保证可重试，状态持久化。
- 直接实现简单逻辑，复用标准库与现有运行时，避免多层小包装。
- go test -race ./...与go vet ./...检查实现，外部数据库只用专用测试连接。
