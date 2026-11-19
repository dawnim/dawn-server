package controllers

import (
	"cmp"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
)

// ResourceHandlers 提供标准资源操作；未提供的操作不注册，Destroy同时处理单条与批量删除。
type ResourceHandlers struct {
	Name                                string // 共用权限前缀的不同资源可指定各自的路由名称前缀
	Index, Store, Show, Update, Destroy gin.HandlerFunc
}

// RegisterResourceRoutes 统一资源方法、id参数、操作权限和路由名称，业务处理仍由调用方提供。
func RegisterResourceRoutes(a *admin.App, g *route.Registrar, path, permission string, h ResourceHandlers) {
	name := cmp.Or(h.Name, permission)
	for _, action := range []struct {
		methods                []string
		path, name, permission string
		handler                gin.HandlerFunc
	}{
		{[]string{"GET"}, path, "index", "list", h.Index},
		{[]string{"POST"}, path, "store", "create", h.Store},
		{[]string{"GET"}, path + "/{id}", "show", "list", h.Show},
		{[]string{"PUT", "PATCH"}, path + "/{id}", "update", "update", h.Update},
		{[]string{"DELETE"}, path, "destroy-many", "delete", h.Destroy},
		{[]string{"DELETE"}, path + "/{id}", "destroy", "delete", h.Destroy},
	} {
		if action.handler != nil {
			g.Match(action.methods, action.path, middleware.Require(a, permission+"."+action.permission), action.handler).Name(name + "." + action.name)
		}
	}
}
