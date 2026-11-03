package controllers

import (
	"strconv"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
)

func (a *Controller) LogRoutes(g *route.Registrar) {

	g.Get("/logs", middleware.Require(a.App, ""), func(c *gin.Context) {
		db := a.DB.Model(&models.Log{})
		if kind := c.Query("kind"); kind != "" {
			db = db.Where("kind = ?", kind)
		}
		for _, field := range []string{"username", "ip", "path", "method", "sender", "recipient"} {
			if value := c.Query(field); value != "" {
				db = db.Where(field+" LIKE ?", "%"+value+"%")
			}
		}
		if status := c.Query("success"); status != "" {
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
		if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var rows []models.Log
		if err := db.Order(response.Order(c, "id", "created_at", "status", "duration")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		for i := range rows {
			rows[i].Location = i18n.Text(c.GetString("admin.locale"), rows[i].Location)
			rows[i].Message = i18n.Text(c.GetString("admin.locale"), rows[i].Message)
		}
		response.Paginated(c, rows, total, page, size)
	})
	remove := func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		if err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			db := tx.Model(&models.Log{})
			if !middleware.Actor(c).User.Super {
				db = db.Where("user_id = ?", middleware.Actor(c).User.ID)
			}
			db = db.Where("id IN ?", ids)
			var total int64
			if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			if total != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return db.Delete(&models.Log{}).Error
		}); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}
	g.Delete("/logs", middleware.Require(a.App, "dawn.logs.delete"), remove)
	g.Delete("/logs/{id}", middleware.Require(a.App, "dawn.logs.delete"), remove)
}
