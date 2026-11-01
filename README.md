# Dawn 服务端

通过 Go Module 接入的后台管理框架。系统功能由依赖维护，业务项目只需编写自己的模型、接口、菜单和页面。

## 功能

- 管理员、角色、部门、岗位、数据权限和登录会话。
- 代码菜单、自定义目录与外链、按钮权限及受控系统登录。
- 参数配置、私有附件、文件秒传、分片上传和 CSV 导入导出。
- 缓存定义与清理、操作日志、服务状态、数据表工具和代码生成。
- HTTP 与 WebSocket 独立端口，多实例消息恢复和确认。
- 组合启动、代码调度与队列监控接入。


## 接入

```sh
go get github.com/dawnim/dawn-server
```

在应用中注册 `dawn.ServiceProvider`，通过 `dawn.RegisterRoutes` 挂载后台接口。框架与业务共用数据库、Redis、文件系统和生命周期。

[创建项目](docs/quickstart.md) 包含启动文件、配置、迁移和初始管理员的完整步骤；前端使用 `@dawnim/admin`。

```sh
go run . migrate
go run . dawn:install
go run . dawn:start all
```

`dawn:install`执行全部系统与项目迁移，并补齐部门、岗位、附件分类、配置和IP地理库。首次安装默认创建 `admin`，密码为 `123456`；需要自定义时在执行命令前设置 `DAWN_ADMIN_PASSWORD`。

管理 API 默认 `/admin/`，HTTP 默认5123，WebSocket默认8851。`all` 守护HTTP、WS、Horizon和cron；`local` 再启动项目的前端，默认访问 `http://127.0.0.1:5173`。服务可分别停止、重启和查看状态。

应用日志默认写入 `storage/logs/dawn.log`，通过 `APP_LOGGER_FILE` 修改。

## 开发文档

- [服务运行](docs/runtime.md)：本地开发、进程控制、平滑重载和生产托管。
- [配置](docs/configuration.md)：数据库、跨域、存储、密钥与运行参数。
- [目录与扩展](docs/structure.md)：控制器、中间件、模型、迁移和业务接口。
- [测试](docs/testing.md)：本地测试、数据库矩阵和验证范围。

## 更新

固定依赖版本，备份数据库，执行 `dawn:install`，再构建前端并重启服务。已保存的菜单和权限 key 保持稳定；变更队列任务类型或编码前先排空旧任务。

## 发布

推送版本标签后自动运行测试与数据库检查，通过后创建GitHub Release。Go Module直接使用该标签。

```sh
git tag -a v0.1.1 -m v0.1.1
git push origin main v0.1.1
```

## 鸣谢

感谢 [PrismGo](https://github.com/prismgo/prismgo) 提供应用容器、数据库、队列与调度能力，以及 Gin、GORM 等开源项目。地理数据来自 [MaxMind GeoLite2](https://www.maxmind.com/en/geolite2/eula)，默认由 [P3TERX/GeoLite.mmdb](https://github.com/P3TERX/GeoLite.mmdb) 下载；使用时遵循相应数据许可。

## 许可

采用 [MIT 许可证](LICENSE)。
