package controllers

import (
	"strconv"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/gin-gonic/gin"
)

func (a *Controller) RealtimeRoutes(g *gin.RouterGroup) {
	g.POST("/realtime/ticket", middleware.Require(a.App, ""), func(c *gin.Context) {
		var input struct {
			Resume string `json:"resume"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "连接参数格式错误")
			return
		}
		current := middleware.Actor(c)
		ticket, err := a.Realtime.Ticket(c.Request.Context(), realtime.Principal{Realm: "admin", Subject: strconv.FormatUint(uint64(current.User.ID), 10), SessionID: current.SessionID}, input.Resume, c.GetString("admin.locale"))
		if err != nil {
			response.Fail(c, 401, "恢复会话无效,请重新建立连接")
			return
		}
		response.Respond(c, gin.H{"ticket": ticket})
	})
}
