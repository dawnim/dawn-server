package controllers

import (
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"github.com/prismgo/horizon"
)

func (a *Controller) HorizonRoutes(g *route.Registrar) {
	manager := horizon.Resolve()
	g.Get("/horizon/configuration", middleware.Require(a.App, "dawn.horizon.list"), func(c *gin.Context) {
		response.Respond(c, gin.H{"api_path": manager.Config().APIPrefix()})
	})
	// 复用上游只读API、聚合与分页，前端仅调整展示布局。
	horizon.RegisterHTTPRoutes(horizon.HTTPOptions{Manager: manager, Auth: []gin.HandlerFunc{middleware.Require(a.App, "dawn.horizon.list")}})
}
