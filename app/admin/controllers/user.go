package controllers

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/authsession"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type userInput struct {
	models.AdminUser
	Password string `json:"password" binding:"omitempty,min=6,max=72"`
	RoleIDs  []uint `json:"role_ids" gorm:"serializer:json"`
	PostIDs  []uint `json:"post_ids" gorm:"serializer:json"`
}

func (a *Controller) UserRoutes(g *route.Registrar) {
	handlers := ResourceHandlers{}
	for resource, permission := range map[string]string{"departments": "dawn.departments", "roles": "dawn.roles", "posts": "dawn.posts"} {
		g.Get("/"+resource+"/options", middleware.Require(a.App, ""), func(c *gin.Context) {
			current := middleware.Actor(c)
			if !current.Can(permission+".list") && !current.Can("dawn.users.list") && !current.Can("dawn.users.create") && !current.Can("dawn.users.update") {
				response.Fail(c, 403, "没有操作权限")
				return
			}
			switch resource {
			case "departments":
				departments := []models.Department{}
				db := a.DB.Select("id", "name", "parent_id").Where("status = ?", models.Enabled)
				if c.Query("managed") == "true" {
					ids, all, err := current.Departments(a.DB)
					if err != nil {
						response.Report(c, err)
						return
					}
					if !all {
						db = db.Where("id IN ?", ids)
					}
				}
				if err := db.Find(&departments).Error; err != nil {
					response.Report(c, err)
					return
				}
				response.Respond(c, departments)
				return
			case "posts":
				posts := []models.Post{}
				if err := a.DB.Select("id", "name").Where("status = ?", models.Enabled).Find(&posts).Error; err != nil {
					response.Report(c, err)
					return
				}
				response.Respond(c, posts)
				return
			}
			var roles []models.Role
			db := a.DB.Select("id", "name").Where("status = ?", models.Enabled)
			if !current.User.Super {
				db = db.Where("level < ?", current.Level())
			}
			if err := db.Order("sort, id").Find(&roles).Error; err != nil {
				response.Report(c, err)
				return
			}
			response.Respond(c, roles)
		})
	}
	query := func(c *gin.Context, connection *gorm.DB) *gorm.DB {
		db := connection.WithContext(c.Request.Context()).Model(&models.AdminUser{}).Scopes(middleware.Actor(c).Scope("admin_users.id", "admin_users.department_id"))
		return db.Preload("Roles").Preload("Posts")
	}
	handlers.Index = func(c *gin.Context) {
		db := a.DB.WithContext(c.Request.Context()).Model(&models.AdminUser{}).Preload("Roles").Preload("Posts")
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
		if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var users []models.AdminUser
		if err := db.Offset((page - 1) * size).Limit(size).Order(response.Order(c, "id", "username", "login_time", "created_at")).Order("id DESC").Find(&users).Error; err != nil {
			response.Report(c, err)
			return
		}
		ids := make([]uint, 0, len(users))
		for _, user := range users {
			ids = append(ids, user.ID)
		}
		var allowed []uint
		if len(ids) > 0 {
			if err := a.DB.Model(&models.AdminUser{}).Scopes(middleware.Actor(c).Scope("admin_users.id", "admin_users.department_id")).Where("id IN ?", ids).Pluck("id", &allowed).Error; err != nil {
				response.Report(c, err)
				return
			}
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
			data = append(data, gin.H{"id": u.ID, "username": u.Username, "realname": u.Realname, "email": u.Email, "phone": u.Phone, "avatar": u.Avatar, "gender": u.Gender, "dept_id": u.DepartmentID, "status": u.Status, "remark": u.Remark, "role_ids": roles, "post_ids": posts, "login_time": u.LoginTime, "is_super": u.Super, "dashboard": u.Dashboard, "can_manage": slices.Contains(allowed, u.ID) && middleware.Actor(c).CanManageUser(&u), "created_at": u.CreatedAt})
		}
		response.Paginated(c, data, total, page, size)
	}
	handlers.Show = func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var user models.AdminUser
		if err := a.DB.Preload("Roles").Preload("Posts").First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, user)
	}
	write := func(c *gin.Context) {
		editing := c.Request.Method != "POST"
		var input userInput
		if editing {
			id := response.ID(c)
			if c.IsAborted() {
				return
			}
			if err := query(c, a.DB).First(&input.AdminUser, id).Error; err != nil {
				response.Report(c, err)
				return
			}
			if !middleware.Actor(c).CanManageUser(&input.AdminUser) {
				response.Fail(c, 403, "不能操作当前账号、超级管理员或同级以上用户")
				return
			}
			for _, role := range input.Roles {
				input.RoleIDs = append(input.RoleIDs, role.ID)
			}
			for _, post := range input.Posts {
				input.PostIDs = append(input.PostIDs, post.ID)
			}
		}
		writable := []string{"username", "realname", "email", "phone", "avatar", "gender", "department_id", "status", "remark", "password", "role_ids", "post_ids"}
		if editing {
			writable = slices.DeleteFunc(writable, func(field string) bool { return field == "password" })
		}
		selected := BindResourceInput(a.DB, c, &input, writable)
		if selected == nil {
			return
		}
		if c.Request.Method == "PUT" {
			selected = writable
		}
		if input.Status != models.Enabled && input.Status != models.Disabled {
			response.Fail(c, 422, "账号状态无效")
			return
		}
		if slices.Contains(selected, "role_ids") {
			if err := a.CheckRoles(middleware.Actor(c), input.RoleIDs); err != nil {
				response.Report(c, err, 403)
				return
			}
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
		departments, all, err := middleware.Actor(c).Departments(a.DB)
		if err != nil {
			response.Report(c, err)
			return
		}
		if !all && !slices.Contains(departments, input.DepartmentID) {
			response.Fail(c, 403, "没有权限操作该部门数据")
			return
		}
		if !editing {
			if input.Password == "" {
				response.Fail(c, 422, "创建账号必须填写密码")
				return
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
			if err != nil {
				response.Report(c, err)
				return
			}
			input.AdminUser.Password = string(hash)
		}
		err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			fields := slices.DeleteFunc(slices.Clone(selected), func(field string) bool { return field == "role_ids" || field == "post_ids" })
			if editing {
				if err := tx.Model(&models.AdminUser{}).Where("id = ?", input.ID).Select(fields).Updates(&input.AdminUser).Error; err != nil {
					return err
				}
			} else {
				if err := CreateResource(tx, &input.AdminUser, []string{"username", "realname", "email", "phone", "avatar", "gender", "department_id", "status", "remark", "password"}, fields); err != nil {
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
			if slices.Contains(selected, "role_ids") {
				if err := tx.Model(&input.AdminUser).Association("Roles").Replace(roles); err != nil {
					return err
				}
			}
			if slices.Contains(selected, "post_ids") {
				if err := tx.Model(&input.AdminUser).Association("Posts").Replace(posts); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		if editing && input.Status == models.Disabled {
			if err := a.Sessions.RevokeUsers(c.Request.Context(), input.ID); err != nil {
				response.Report(c, err)
				return
			}
		}
		if err := a.ClearUserCaches(c.Request.Context(), input.ID); err != nil {
			response.Report(c, err)
			return
		}
		if editing {
			response.Respond(c, input.AdminUser)
		} else {
			response.Created(c, input.AdminUser, fmt.Sprintf("%s/%d", c.Request.URL.Path, input.ID))
		}
	}
	handlers.Store, handlers.Update = write, write
	handlers.Destroy = func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		denied := ""
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var users []models.AdminUser
			if err := query(c, tx).Where("id IN ?", ids).Find(&users).Error; err != nil {
				return err
			}
			if len(users) != len(ids) {
				return gorm.ErrRecordNotFound
			}
			for _, user := range users {
				if !middleware.Actor(c).CanManageUser(&user) {
					denied = "不能操作当前账号、超级管理员或同级以上用户"
					return errors.New(denied)
				}
			}
			return tx.Delete(&models.AdminUser{}, "id IN ?", ids).Error
		})
		if err != nil {
			if denied != "" {
				response.Fail(c, 403, denied)
			} else {
				response.Report(c, err)
			}
			return
		}
		if err := a.Sessions.RevokeUsers(c.Request.Context(), ids...); err != nil {
			response.Report(c, err)
			return
		}
		if err := a.ClearUserCaches(c.Request.Context(), ids...); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}

	g.Put("/users/{id}/password", middleware.Require(a.App, "dawn.users.password"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var user models.AdminUser
		if err := query(c, a.DB).First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		if !middleware.Actor(c).CanManageUser(&user) {
			response.Fail(c, 403, "不能操作当前账号、超级管理员或同级以上用户")
			return
		}
		var input struct {
			Password string `json:"password" binding:"required,min=6,max=72"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "新密码长度须为6至72字符")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			response.Report(c, err)
			return
		}
		if err := a.DB.WithContext(c.Request.Context()).Model(&models.AdminUser{}).Where("id = ?", id).Update("password", string(hash)).Error; err != nil {
			response.Report(c, err)
			return
		}
		if err := a.Sessions.RevokeUsers(c.Request.Context(), id); err != nil {
			response.Report(c, err)
			return
		}
		if err := a.ClearUserCaches(c.Request.Context(), id); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	})
	for _, action := range []string{"dashboard", "cache"} {
		method := "PUT"
		if action == "cache" {
			method = "DELETE"
		}
		g.Match([]string{method}, "/users/{id}/"+action, middleware.Require(a.App, "dawn.users."+action), func(c *gin.Context) {
			id := response.ID(c)
			if c.IsAborted() {
				return
			}
			var user models.AdminUser
			if err := query(c, a.DB).First(&user, id).Error; err != nil {
				response.Report(c, err)
				return
			}
			if !middleware.Actor(c).CanManageUser(&user) {
				response.Fail(c, 403, "不能操作当前账号、超级管理员或同级以上用户")
				return
			}
			if action == "dashboard" {
				var input struct {
					Dashboard string `json:"dashboard" binding:"required,oneof=statistics work"`
				}
				if c.ShouldBindJSON(&input) != nil {
					response.Fail(c, 422, "工作台类型无效")
					return
				}
				if err := a.DB.Model(&user).Update("dashboard", input.Dashboard).Error; err != nil {
					response.Report(c, err)
					return
				}
			}
			if err := a.ClearUserCaches(c.Request.Context(), id); err != nil {
				response.Report(c, err)
				return
			}
			if _, err := a.Realtime.Publish(c.Request.Context(), realtime.Principal{Realm: "admin", Subject: strconv.FormatUint(uint64(id), 10)}, "user.refresh", nil); err != nil {
				response.Report(c, err)
				return
			}
			c.Status(204)
		})
	}
	g.Get("/sessions", middleware.Require(a.App, "dawn.users.list"), func(c *gin.Context) {
		var userID uint
		if !middleware.Actor(c).User.Super {
			userID = middleware.Actor(c).User.ID
		}
		cursor, err := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
		if err != nil {
			response.Fail(c, 422, "游标格式错误")
			return
		}
		_, size := response.Page(c)
		rows, next, err := a.Sessions.List(c.Request.Context(), userID, cursor, int64(size))
		if err != nil {
			response.Report(c, err)
			return
		}
		slices.SortFunc(rows, func(a, b authsession.Record) int {
			if n := cmp.Compare(b.CreatedAt, a.CreatedAt); n != 0 {
				return n
			}
			return cmp.Compare(b.ID, a.ID)
		})
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"cursor": strconv.FormatUint(next, 10)}})
	})
	g.Delete("/sessions/{session}", middleware.Require(a.App, "dawn.users.update"), func(c *gin.Context) {
		row, err := a.Sessions.Read(c.Request.Context(), c.Param("session"))
		if errors.Is(err, authsession.ErrUnauthenticated) {
			c.Status(204)
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		if !middleware.Actor(c).User.Super && row.UserID != middleware.Actor(c).User.ID {
			response.Fail(c, 403, "没有操作权限")
			return
		}
		if err = a.Sessions.Revoke(c.Request.Context(), row.ID); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	})
	RegisterResourceRoutes(a.App, g, "/users", "dawn.users", handlers)
}
