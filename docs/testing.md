# 测试

## 常规检查

```sh
go test -race ./...
go vet ./...
```

测试使用临时SQLite库和隔离Redis实例，覆盖鉴权、角色授权、数据范围、参数加密、附件预检与分片、缓存策略、CSV、CORS和组合服务退出。WS测试覆盖消息确认、跨实例重连和补发。

## 数据库矩阵

使用明确指定的专用空库：

```sh
DAWN_TEST_DRIVER=pgsql DAWN_TEST_DSN='专用测试库DSN' \
  go test -run 'Test(Application|DatabaseMatrix)$' -count=1 ./tests
```

驱动可选mysql、mariadb、pgsql。完整接口测试会创建固定测试账号和菜单，重复执行使用新的空库。未指定连接时，Matrix测试跳过，普通接口测试继续使用临时SQLite。

## 运行验证

编译后检查组合入口的HTTP健康状态、WS握手、队列消费和定时任务。发送SIGTERM确认主进程和worker收尾；模拟端口占用或服务退出，确认单独退避恢复；stop后不自动拉起，reload失败保留旧进程。管理器被强杀后，子服务应感知控制管道关闭并退出。

前端联调检查跨域预检、Bearer请求、iframe、文件下载、权限显隐和页面覆盖。跨主机存储、Redis故障恢复、SMTP与容量按实际部署环境验收。
