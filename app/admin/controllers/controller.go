package controllers

import "github.com/dawnim/dawn-server/app/admin"

// Controller 持有后台模块共享的连接和定义。
type Controller struct{ *admin.App }
