package controllers

import (
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (a *Controller) LogRoutes(g *gin.RouterGroup) {

	g.GET("/system/logs", middleware.Require(a.App, ""), func(c *gin.Context) {
		db := a.DB.Model(&models.Log{})
		if kind := c.Query("kind"); kind != "" {
			db = db.Where("kind = ?", kind)
		}
		for _, field := range []string{"username", "ip", "path", "method", "sender", "recipient"} {
			if value := c.Query(field); value != "" {
				db = db.Where(field+" LIKE ?", "%"+value+"%")
			}
		}
		if status := c.Query("status"); status != "" {
			if status == "1" {
				db = db.Where("status < ?", 400)
			} else {
				db = db.Where("status >= ?", 400)
			}
		}
		for _, bound := range []struct{ key, op string }{{"start", ">="}, {"end", "<="}} {
			if value := c.Query(bound.key); value != "" {
				timestamp, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					response.Fail(c, 422, "参数格式错误")
					return
				}
				db = db.Where("created_at "+bound.op+" ?", timestamp)
			}
		}
		// 无日志管理权限时, 个人中心仅能读取当前账号的日志.
		if c.Query("self") == "1" || !middleware.Actor(c).User.Super {
			db = db.Where("user_id = ?", middleware.Actor(c).User.ID)
		}
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var rows []models.Log
		if err := db.Order(response.Order(c, "id", "created_at", "status", "duration")).Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		for i := range rows {
			rows[i].Location = i18n.Text(c.GetString("admin.locale"), rows[i].Location)
			rows[i].Message = i18n.Text(c.GetString("admin.locale"), rows[i].Message)
		}
		response.Respond(c, gin.H{"data": rows, "total": total, "current_page": page, "per_page": size})
	})
	g.DELETE("/system/logs/:id", middleware.Require(a.App, "dawn.logs.delete"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		db := a.DB
		if !middleware.Actor(c).User.Super {
			db = db.Where("user_id = ?", middleware.Actor(c).User.ID)
		}
		if err := db.Delete(&models.Log{}, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})

}
