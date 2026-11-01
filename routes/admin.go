package routes

import (
	"strings"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/controllers"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/framework/logger"
)

func Register(application *foundation.Application, e *gin.Engine) error {
	a, err := admin.From(application.Container())
	if err != nil {
		return err
	}
	a.Router = e
	controller := &controllers.Controller{App: a}
	e.Use(middleware.Locale, middleware.CORS(a.Options.AllowedOrigins), middleware.Audit(a.DB))
	e.GET("/health", func(c *gin.Context) {
		db, err := a.DB.DB()
		if err == nil {
			err = db.PingContext(c.Request.Context())
		}
		if err == nil {
			err = a.Redis.Ping(c.Request.Context()).Err()
		}
		if err != nil {
			logger.Error("健康检查失败", map[string]any{"error": err})
			c.JSON(503, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	g := e.Group(a.Options.AdminPrefix)
	controller.AuthRoutes(g)
	controller.UserRoutes(g)
	controller.RoleRoutes(g)
	controller.SettingRoutes(g)
	controller.AttachmentRoutes(g)
	controller.CacheRoutes(g)
	controller.ServerRoutes(g)
	controller.HorizonRoutes(g)
	controller.DashboardRoutes(g)
	controller.LogRoutes(g)
	controller.MailRoutes(g)
	controller.DatabaseRoutes(g)
	controller.GeneratorRoutes(g)
	controller.RealtimeRoutes(g)
	controllers.RegisterResource(a, g, controllers.Resource[models.Department]{Path: "/system/departments", Permission: "dawn.departments", Fields: []string{"name", "parent_id", "leader", "phone", "email", "sort", "status", "remark"}, Search: []string{"name"}, Tree: true})
	controllers.RegisterResource(a, g, controllers.Resource[models.Post]{Path: "/system/posts", Permission: "dawn.posts", Fields: []string{"name", "code", "sort", "status", "remark"}, Search: []string{"name", "code"}, Importable: true})
	controllers.RegisterResource(a, g, controllers.Resource[models.Category]{Path: "/attachments/categories", Permission: "dawn.attachments", Fields: []string{"name", "parent_id", "sort"}, Search: []string{"name"}, Tree: true})
	g.GET("/menus", middleware.Require(a, ""), func(c *gin.Context) {
		menus, err := a.Menus(middleware.Actor(c), c.GetString("admin.locale"))
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, menus)
	})
	g.GET("/system/menus/definitions", middleware.Require(a, "dawn.menus.list"), func(c *gin.Context) {
		var custom []models.Menu
		if err := a.DB.Order("sort, id").Find(&custom).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"menus": a.MenuList(c.GetString("admin.locale")), "custom": custom, "permissions": a.PermissionList(c.GetString("admin.locale"))})
	})
	g.GET("/system/menus/available", middleware.Require(a, "dawn.menus.list"), func(c *gin.Context) {
		key := c.Query("key")
		available := admin.StableKey.MatchString(key) && strings.HasPrefix(key, "custom.")
		for _, m := range a.MenuDefinitions {
			if m.Key == key {
				available = false
			}
		}
		if _, exists := a.PermissionDefinitions[key]; exists {
			available = false
		}
		var count int64
		if err := a.DB.Unscoped().Model(&models.Menu{}).Where(map[string]any{"key": key}).Where("id <> ?", c.DefaultQuery("id", "0")).Count(&count).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"available": available && count == 0})
	})
	controllers.RegisterResource(a, g, controllers.Resource[models.Menu]{Path: "/system/menus", Permission: "dawn.menus", Fields: []string{"type", "key", "parent_key", "title", "path", "component", "status", "hidden", "keep_alive", "fixed_tab", "full_page", "url", "icon", "mode", "sort", "system_key"}, Search: []string{"title"}, Validate: a.ValidateMenu})
	controller.LinkRoutes(g)
	if a.Options.Routes != nil {
		a.Options.Routes(a, g)
	}
	if a.Options.PublicRoutes != nil {
		a.Options.PublicRoutes(a, e)
	}
	return nil
}
