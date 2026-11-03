# Dawn

**像写 Laravel 一样，轻松构建 Go 管理系统。**

基于 Go、Vue3 与 Element Plus 的全栈后台开发框架。集成权限管理、代码生成、任务调度与实时通信，从业务开发到服务运行，开箱即用。

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Vue](https://img.shields.io/badge/Vue-3-42B883?logo=vuedotjs&logoColor=white)](https://vuejs.org/)
[![TypeScript](https://img.shields.io/badge/TypeScript-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org/)
[![License](https://img.shields.io/badge/License-MIT-blue)](LICENSE)

[快速开始](https://dawnim.github.io/dawn-simple/quickstart.html) · [开发文档](https://dawnim.github.io/dawn-simple/) · [最佳实践](https://github.com/dawnim/dawn-simple)

## 核心特性

- **熟悉的开发体验**：Laravel 风格的路由、服务容器、数据库迁移、事件与队列，用 Go 专注业务。
- **完整的权限体系**：角色授权、菜单按钮、部门数据范围与管理级别，覆盖界面和接口。
- **高效的代码生成**：从数据表生成后端接口、前端页面、字段语言包与迁移，支持组件引用和源码定制。
- **独立的业务扩展**：框架通过 Go Module 和 npm 包接入，业务模型、菜单、页面与配置留在自己的项目。
- **实时消息与协作**：内置管理员聊天、未读提醒、已读同步，WebSocket 支持业务事件、频道订阅与断线恢复。
- **任务调度与监控**：事件、异步队列、定时任务和 Horizon，串行与并发任务按需编排。
- **完整的服务管理**：一条命令启动前后端，支持进程守护、HTTP/WS 平滑重载与本地终端接管。
- **现代化管理界面**：Vue3、TypeScript、亮暗主题、多语言、响应式布局与可定制工作台。

## 功能模块

| 模块 | 能力 |
|---|---|
| 组织与权限 | 管理员、角色、部门、岗位、数据范围、菜单与按钮授权 |
| 系统配置 | 站点、上传、邮件与业务参数，保密配置加密存储 |
| 文件管理 | 分类、秒传、分片上传、私密访问、本地与多种对象存储 |
| 实时协作 | 管理员单聊、联系人搜索、消息历史、未读与已读同步 |
| 开发工具 | CRUD 代码生成、字段映射、CSV 导入导出、业务页面扩展 |
| 运维管理 | 服务监控、缓存管理、数据表维护、登录与操作日志 |
| 任务管理 | Horizon 队列监控、任务与批次查询、定时调度 |
| 工作台 | 统计页、员工工作台、自定义图表与顶栏工具 |

## 项目组成

| 仓库 | 职责 |
|---|---|
| [dawn-server](https://github.com/dawnim/dawn-server) | Go 服务端框架，提供后台接口、授权、实时通信与服务管理 |
| [dawn-admin](https://github.com/dawnim/dawn-admin) | Vue 前端组件库，提供管理页面、公共组件与业务接入接口 |
| [dawn-simple](https://github.com/dawnim/dawn-simple) | 完整应用模板、业务示例与开发文档 |

## 最佳实践

[dawn-simple](https://github.com/dawnim/dawn-simple) 提供完整的应用结构与业务示例，涵盖前后端接入、权限配置、缓存定义、实时通信和服务部署，可直接作为新项目的开发起点。

## 开发文档

在线文档：[https://dawnim.github.io/dawn-simple/](https://dawnim.github.io/dawn-simple/)

- [快速开始](https://github.com/dawnim/dawn-simple/blob/main/docs/quickstart.md)：安装、初始化与首次运行。
- [架构与目录](https://github.com/dawnim/dawn-simple/blob/main/docs/architecture.md)：项目结构、应用生命周期与扩展入口。
- [后端开发](https://github.com/dawnim/dawn-simple/blob/main/docs/backend.md) / [前端开发](https://github.com/dawnim/dawn-simple/blob/main/docs/frontend.md)：从接口、菜单到业务页面。
- [权限与数据范围](https://github.com/dawnim/dawn-simple/blob/main/docs/authorization.md)：角色授权、管理级别与数据归属。
- [聊天与实时通信](https://github.com/dawnim/dawn-simple/blob/main/docs/chat.md)：消息收发、可靠投递与 WebSocket 扩展。
- [配置](https://github.com/dawnim/dawn-simple/blob/main/docs/configuration.md) / [服务运行](https://github.com/dawnim/dawn-simple/blob/main/docs/runtime.md) / [测试与发布](https://github.com/dawnim/dawn-simple/blob/main/docs/testing.md)。

## 鸣谢

- [PrismGo](https://github.com/prismgo/prismgo)：Laravel 风格的 Go 应用框架与基础组件。
- [SaiAdmin 6.x](https://github.com/saithink/saiadmin6.x)、[Art Design Pro](https://github.com/Daymychen/art-design-pro)：管理界面、布局与组件基础。
- [Gin](https://github.com/gin-gonic/gin)、[GORM](https://gorm.io/)、[Vue](https://vuejs.org/)、[Element Plus](https://element-plus.org/) 及相关开源项目。
- IP 地理数据来自 [MaxMind GeoLite2](https://www.maxmind.com/en/geolite2/eula)，默认通过 [P3TERX/GeoLite.mmdb](https://github.com/P3TERX/GeoLite.mmdb) 下载，数据使用遵循其许可。

## 许可

采用 [MIT 许可证](LICENSE)。使用与分发时保留各项目及上游代码的版权和许可声明。
