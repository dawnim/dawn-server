package controllers

import (
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type userInput struct {
	models.User
	Password string `json:"password" binding:"omitempty,min=6,max=72"`
	RoleIDs  []uint `json:"role_ids"`
	PostIDs  []uint `json:"post_ids"`
}

func (a *Controller) UserRoutes(g *gin.RouterGroup) {
	g.GET("/system/user-options", middleware.Require(a.App, "dawn.users.list"), func(c *gin.Context) {
		var departments []models.Department
		db := a.DB.Select("id", "name", "parent_id").Where("status = ?", models.Enabled)
		if !middleware.Actor(c).User.Super {
			db = db.Where("id = ?", middleware.Actor(c).User.DepartmentID)
		}
		if err := db.Find(&departments).Error; err != nil {
			response.Report(c, err)
			return
		}
		var roles []models.Role
		db = a.DB.Where("status = ?", models.Enabled)
		if !middleware.Actor(c).User.Super {
			db = db.Where("level > ? AND data_scope = ?", middleware.Actor(c).Level(), models.ScopeSelf)
		}
		if err := db.Find(&roles).Error; err != nil {
			response.Report(c, err)
			return
		}
		ids := []uint{}
		for _, r := range roles {
			ids = append(ids, r.ID)
		}
		var grants []models.RolePermission
		if len(ids) > 0 {
			if err := a.DB.Where("role_id IN ?", ids).Find(&grants).Error; err != nil {
				response.Report(c, err)
				return
			}
		}
		choices := []gin.H{}
		for _, role := range roles {
			allowed := true
			for _, grant := range grants {
				if grant.RoleID == role.ID && !middleware.Actor(c).Can(grant.Key) {
					allowed = false
					break
				}
			}
			if allowed {
				choices = append(choices, gin.H{"id": role.ID, "name": role.Name})
			}
		}
		var posts []models.Post
		if err := a.DB.Select("id", "name").Where("status = ?", models.Enabled).Find(&posts).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"departments": departments, "roles": choices, "posts": posts})
	})
	query := func(c *gin.Context) *gorm.DB {
		db := a.DB.Model(&models.User{}).Scopes(middleware.Actor(c).Scope("admin_users.id", "admin_users.department_id"))
		if !middleware.Actor(c).User.Super {
			db = db.Where("super = ?", false)
		}
		return db.Preload("Roles").Preload("Posts")
	}
	g.GET("/system/users", middleware.Require(a.App, "dawn.users.list"), func(c *gin.Context) {
		db := query(c)
		for _, field := range []string{"username", "phone", "email"} {
			if v := c.Query(field); v != "" {
				db = db.Where(field+" LIKE ?", "%"+v+"%")
			}
		}
		if v := c.Query("dept_id"); v != "" {
			db = db.Where("department_id = ?", v)
		}
		if v := c.Query("status"); v != "" {
			db = db.Where("status = ?", v)
		}
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var users []models.User
		if err := db.Offset((page - 1) * size).Limit(size).Order(response.Order(c, "id", "username", "login_time", "created_at")).Find(&users).Error; err != nil {
			response.Report(c, err)
			return
		}
		data := make([]gin.H, 0, len(users))
		for _, u := range users {
			roles, posts := []uint{}, []uint{}
			for _, r := range u.Roles {
				roles = append(roles, r.ID)
			}
			for _, p := range u.Posts {
				posts = append(posts, p.ID)
			}
			data = append(data, gin.H{"id": u.ID, "username": u.Username, "realname": u.Realname, "email": u.Email, "phone": u.Phone, "avatar": u.Avatar, "gender": u.Gender, "dept_id": u.DepartmentID, "status": u.Status, "remark": u.Remark, "role_ids": roles, "post_ids": posts, "login_time": u.LoginTime, "is_super": u.Super, "created_at": u.CreatedAt})
		}
		response.Respond(c, gin.H{"data": data, "total": total, "current_page": page, "per_page": size})
	})
	g.GET("/system/users/:id", middleware.Require(a.App, "dawn.users.list"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var user models.User
		if err := query(c).First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, user)
	})
	for _, editing := range []bool{false, true} {
		method, path, permission := "POST", "/system/users", "dawn.users.create"
		if editing {
			method, path, permission = "PUT", "/system/users/:id", "dawn.users.update"
		}
		g.Handle(method, path, middleware.Require(a.App, permission), func(c *gin.Context) {
			var input userInput
			if c.ShouldBindJSON(&input) != nil {
				response.Fail(c, 422, "请检查账号和密码格式")
				return
			}
			input.Base = models.Base{}
			input.Super = false
			if input.Status != models.Enabled && input.Status != models.Disabled {
				response.Fail(c, 422, "账号状态无效")
				return
			}
			if err := a.CheckRoles(middleware.Actor(c), input.RoleIDs); err != nil {
				response.Report(c, err, 403)
				return
			}
			if input.DepartmentID > 0 {
				var count int64
				if err := a.DB.Model(&models.Department{}).Where("id = ?", input.DepartmentID).Count(&count).Error; err != nil {
					response.Report(c, err)
					return
				}
				if count == 0 {
					response.Fail(c, 422, "部门不存在")
					return
				}
			}
			if !middleware.Actor(c).User.Super && input.DepartmentID != middleware.Actor(c).User.DepartmentID {
				response.Fail(c, 403, "只能管理本部门的账号")
				return
			}
			if editing {
				id := response.ID(c)
				if c.IsAborted() {
					return
				}
				var old models.User
				if err := query(c).First(&old, id).Error; err != nil {
					response.Report(c, err)
					return
				}
				if old.Super && !middleware.Actor(c).User.Super {
					response.Fail(c, 403, "不能修改超级管理员")
					return
				}
				if !middleware.Actor(c).User.Super {
					for _, r := range old.Roles {
						if r.Level <= middleware.Actor(c).Level() {
							response.Fail(c, 403, "不能修改同级或更高级用户")
							return
						}
					}
				}
				if old.ID == middleware.Actor(c).User.ID && input.Status != models.Enabled {
					response.Fail(c, 422, "不能停用当前账号")
					return
				}
				input.ID = id
			} else if input.Password == "" {
				response.Fail(c, 422, "创建账号必须填写密码")
				return
			}
			password := ""
			if input.Password != "" {
				hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
				if err != nil {
					response.Report(c, err)
					return
				}
				password = string(hash)
			}
			err := a.DB.Transaction(func(tx *gorm.DB) error {
				fields := []string{"username", "realname", "email", "phone", "avatar", "gender", "department_id", "status", "remark"}
				if password != "" {
					input.User.Password = password
					fields = append(fields, "password")
				}
				if editing {
					if err := tx.Model(&models.User{}).Where("id = ?", input.ID).Select(fields).Updates(&input.User).Error; err != nil {
						return err
					}
				} else {
					if err := tx.Select(append(fields, "CreatedAt", "UpdatedAt", "DeletedAt")).Create(&input.User).Error; err != nil {
						return err
					}
				}
				var roles []models.Role
				if len(input.RoleIDs) > 0 {
					if err := tx.Where("id IN ?", input.RoleIDs).Find(&roles).Error; err != nil {
						return err
					}
				}
				var posts []models.Post
				if len(input.PostIDs) > 0 {
					if err := tx.Where("id IN ?", input.PostIDs).Find(&posts).Error; err != nil {
						return err
					}
				}
				if err := tx.Model(&input.User).Association("Roles").Replace(roles); err != nil {
					return err
				}
				if err := tx.Model(&input.User).Association("Posts").Replace(posts); err != nil {
					return err
				}
				if editing && (password != "" || input.Status == models.Disabled) {
					return tx.Delete(&models.Session{}, "user_id = ?", input.ID).Error
				}
				return nil
			})
			if err != nil {
				response.Report(c, err)
				return
			}
			response.Respond(c, input.User)
		})
	}
	g.DELETE("/system/users/:id", middleware.Require(a.App, "dawn.users.delete"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var user models.User
		if err := query(c).First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		if user.Super || id == middleware.Actor(c).User.ID {
			response.Fail(c, 403, "不能删除超级管理员或当前账号")
			return
		}
		if !middleware.Actor(c).User.Super {
			for _, r := range user.Roles {
				if r.Level <= middleware.Actor(c).Level() {
					response.Fail(c, 403, "不能删除同级或更高级用户")
					return
				}
			}
		}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&user).Error; err != nil {
				return err
			}
			return tx.Delete(&models.Session{}, "user_id = ?", id).Error
		}); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.GET("/system/sessions", middleware.Require(a.App, "dawn.users.list"), func(c *gin.Context) {
		var rows []models.Session
		db := a.DB.Where("expires_at > ?", time.Now().UnixMilli())
		if !middleware.Actor(c).User.Super {
			db = db.Where("user_id = ?", middleware.Actor(c).User.ID)
		}
		if err := db.Order("created_at DESC").Limit(200).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, rows)
	})
	g.DELETE("/system/sessions/:session", middleware.Require(a.App, "dawn.users.update"), func(c *gin.Context) {
		db := a.DB.Where("id = ?", c.Param("session"))
		if !middleware.Actor(c).User.Super {
			db = db.Where("user_id = ?", middleware.Actor(c).User.ID)
		}
		if err := db.Delete(&models.Session{}).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
}
