package controllers

import (
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func (a *Controller) MemberRoutes(g *route.Registrar) {
	handlers := ResourceHandlers{}
	fields := []string{"nickname", "avatar", "email", "mobile", "locale", "status"}
	handlers.Index = func(c *gin.Context) {
		var filters struct {
			Query  string `form:"query"`
			Type   string `form:"type" binding:"omitempty,oneof=guest member ai"`
			Status string `form:"status" binding:"omitempty,oneof=1 2"`
		}
		if c.ShouldBindQuery(&filters) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		q := a.DB.WithContext(c.Request.Context()).Model(&models.User{})
		if value := filters.Query; value != "" {
			q = q.Where("nickname LIKE ? OR username LIKE ? OR unique_id = ?", "%"+value+"%", "%"+value+"%", value)
		}
		if value := filters.Type; value != "" {
			q = q.Where("type = ?", value)
		}
		if value := filters.Status; value != "" {
			q = q.Where("status = ?", value)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.User{}
		if err := q.Order(response.Order(c, "id", "unique_id", "nickname", "created_at")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	}
	handlers.Show = func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var user models.User
		if err := a.DB.WithContext(c.Request.Context()).First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, user)
	}
	handlers.Store = func(c *gin.Context) {
		var input struct {
			models.User
			Password string `json:"password" binding:"required,min=6,max=72"`
		}
		if BindResourceInput(a.DB, c, &input, append([]string{"username", "password"}, fields...)) == nil {
			return
		}
		if input.Username == nil || len(input.Password) > 72 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			response.Report(c, err)
			return
		}
		user := input.User
		user.Type, user.PasswordHash = models.UserMember, string(hash)
		if err = a.DB.WithContext(c.Request.Context()).Create(&user).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, user, c.Request.URL.Path+"/"+strconv.FormatUint(uint64(user.ID), 10))
	}
	handlers.Update = func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var user models.User
		if err := a.DB.WithContext(c.Request.Context()).First(&user, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		selected := BindResourceInput(a.DB, c, &user, fields)
		if selected == nil {
			return
		}
		if c.Request.Method == "PUT" {
			selected = fields
		}
		if err := a.DB.WithContext(c.Request.Context()).Model(&user).Select(selected).Updates(&user).Error; err != nil {
			response.Report(c, err)
			return
		}
		if user.Status == models.Disabled {
			if err := a.Customer.Sessions.RevokeUsers(c.Request.Context(), id); err != nil {
				response.Report(c, err)
				return
			}
		}
		response.Respond(c, user)
	}
	handlers.Destroy = func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var total int64
			if err := tx.Model(&models.User{}).Where("id IN ?", ids).Count(&total).Error; err != nil {
				return err
			}
			if total != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return tx.Where("id IN ?", ids).Delete(&models.User{}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		if err := a.Customer.Sessions.RevokeUsers(c.Request.Context(), ids...); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}

	g.Patch("/support-conversations/{id}/customer", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		conv, err := a.Customer.Conversation(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, "reply")
		if err != nil {
			response.Report(c, err)
			return
		}
		var input struct {
			Remark string `json:"remark"`
		}
		if c.ShouldBindJSON(&input) != nil || len(input.Remark) > 16000 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err = a.DB.WithContext(c.Request.Context()).Model(&models.UserTraits{}).Where("app_id = ? AND user_id = ?", conv.AppID, conv.UserID).Updates(map[string]any{"remark": input.Remark, "last_seen_at": time.Now().UnixMilli()}).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	RegisterResourceRoutes(a.App, g, "/members", "dawn.members", handlers)
}
