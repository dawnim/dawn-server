在 app/admin/menu.go 登记 app.@@table@@ 的 list、create、update、delete 权限及菜单。在 app/admin/routes.go 调用 controllers.Register@@name@@。

前端在 createAdmin 的 pages 中登记：

```ts
'/business/@@table@@': () => import('./views/business/@@table@@/index.vue')
```

API与字段定义位于 admin/src/api/business/@@table@@.ts。引用模式使用内置 CrudPage；源码模式包含列表、modules/table-search.vue 与 modules/edit-dialog.vue，可直接修改。

使用前检查字段类型、必填规则及 Scope/Prepare。业务表使用 id 主键和 created_at、updated_at、deleted_at 整数时间字段。

字段映射位于 admin/src/locales/zh/@@table@@.ts 和 en/@@table@@.ts，由页面自动加载。字段标识作为映射key，接口字段名保持不变。固定提示使用中文原文key，缺少译文时仍渲染占位符。

勾选“生成表迁移”会在 database/migrations 生成当前结构快照，已有表跳过建表。后续新增或修改字段需添加增量迁移，再运行迁移命令。检查业务约束后部署。
