package controllers

import (
	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *Controller) RoleRoutes(g *gin.RouterGroup) {
	RegisterResource(a.App, g, Resource[models.Role]{Path: "/system/roles", Permission: "dawn.roles", Fields: []string{"name", "code", "level", "status", "sort", "remark", "data_scope", "department_ids"}, Search: []string{"name", "code"},
		Scope: func(db *gorm.DB, current *admin.Actor) *gorm.DB {
			if current.User.Super {
				return db
			}
			return db.Where("level > ?", current.Level())
		},
		Validate: func(role *models.Role, editing bool) error {
			if role.Level < 1 {
				return i18n.Errorf("角色级别至少为1")
			}
			if role.Status != models.Enabled && role.Status != models.Disabled {
				return i18n.Errorf("角色状态无效")
			}
			switch role.DataScope {
			case models.ScopeAll, models.ScopeSelf, models.ScopeDepartment, models.ScopeDepartments:
			default:
				return i18n.Errorf("数据权限范围无效")
			}
			return nil
		},
	})
	g.GET("/system/roles/:id/permissions", middleware.Require(a.App, "dawn.roles.list"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var role models.Role
		if err := a.DB.First(&role, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		if !middleware.Actor(c).User.Super && role.Level <= middleware.Actor(c).Level() {
			response.Fail(c, 403, "不能查看同级或更高级角色授权")
			return
		}
		var keys []string
		if err := a.DB.Model(&models.RolePermission{}).Where("role_id = ?", id).Pluck("key", &keys).Error; err != nil {
			response.Report(c, err)
			return
		}
		permissions := []admin.Permission{}
		for _, p := range a.PermissionList(c.GetString("admin.locale")) {
			if middleware.Actor(c).Can(p.Key) {
				permissions = append(permissions, p)
			}
		}
		var menus []models.Menu
		if err := a.DB.Find(&menus).Error; err != nil {
			response.Report(c, err)
			return
		}
		for _, m := range menus {
			if m.Type != models.MenuDirectory && middleware.Actor(c).Can(m.Key) {
				permissions = append(permissions, admin.Permission{Key: m.Key, Title: m.Title, ParentKey: m.ParentKey})
			}
		}
		response.Respond(c, gin.H{"keys": keys, "permissions": permissions, "menus": a.MenuList(c.GetString("admin.locale")), "custom": menus, "role": role})
	})
	g.PUT("/system/roles/:id/permissions", middleware.Require(a.App, "dawn.roles.update"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var input struct {
			Keys []string `json:"keys"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "授权数据格式错误")
			return
		}
		var role models.Role
		if err := a.DB.First(&role, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		if !middleware.Actor(c).User.Super && role.Level <= middleware.Actor(c).Level() {
			response.Fail(c, 403, "不能修改同级或更高级角色")
			return
		}
		known := map[string]bool{}
		for key := range a.PermissionDefinitions {
			known[key] = true
		}
		var menus []models.Menu
		if err := a.DB.Where("type <> ?", models.MenuDirectory).Find(&menus).Error; err != nil {
			response.Report(c, err)
			return
		}
		for _, m := range menus {
			known[m.Key] = true
		}
		grants := []models.RolePermission{}
		seen := map[string]bool{}
		for _, key := range input.Keys {
			if !known[key] || !middleware.Actor(c).Can(key) {
				response.Fail(c, 403, "包含未注册或超出自身范围的权限")
				return
			}
			if !seen[key] {
				grants = append(grants, models.RolePermission{RoleID: id, Key: key})
				seen[key] = true
			}
		}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&models.RolePermission{}, "role_id = ?", id).Error; err != nil {
				return err
			}
			if len(grants) > 0 {
				return tx.Create(&grants).Error
			}
			return nil
		}); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
}
