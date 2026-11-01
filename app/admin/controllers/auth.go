package controllers

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"image/color"
	"image/png"
	"net/http"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dchest/captcha"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func (a *Controller) AuthRoutes(g *gin.RouterGroup) {
	g.GET("/auth/captcha", func(c *gin.Context) {
		id := rand.Text()
		digits := captcha.RandomDigits(5)
		text := make([]byte, len(digits))
		for i, d := range digits {
			text[i] = '0' + d
		}
		if err := a.Redis.Set(c.Request.Context(), a.Key(constants.TempCaptcha, id), string(text), constants.TempCaptcha.TTL).Err(); err != nil {
			response.Report(c, err)
			return
		}
		var image bytes.Buffer
		picture := captcha.NewImage(id, digits, 180, 60)
		picture.Palette[0] = color.White
		if err := png.Encode(&image, picture); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"uuid": id, "image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image.Bytes())})
	})
	g.POST("/auth/login", func(c *gin.Context) {
		var input struct {
			Username string `json:"username" binding:"required,max=80"`
			Password string `json:"password" binding:"required,max=72"`
			UUID     string `json:"uuid" binding:"required"`
			Code     string `json:"code" binding:"required"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "请填写账号、密码和验证码")
			return
		}
		ctx := c.Request.Context()
		limit := a.Key(constants.TempLoginLimit, admin.TokenHash(c.ClientIP()+":"+input.Username))
		attempts, err := a.Redis.Get(ctx, limit).Int()
		if err != nil && !errors.Is(err, redis.Nil) {
			response.Report(c, err)
			return
		}
		if attempts >= 10 {
			response.Fail(c, 429, "登录尝试过多,请稍后重试")
			return
		}
		answer, err := a.Redis.GetDel(ctx, a.Key(constants.TempCaptcha, input.UUID)).Result()
		if errors.Is(err, redis.Nil) || (err == nil && answer != input.Code) {
			response.Fail(c, 422, "验证码错误或已过期")
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		var user models.User
		err = a.DB.Where("username = ? AND status = ?", input.Username, models.Enabled).First(&user).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			response.Report(c, err)
			return
		}
		if err != nil || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)) != nil {
			pipe := a.Redis.TxPipeline()
			pipe.Incr(ctx, limit)
			pipe.Expire(ctx, limit, constants.TempLoginLimit.TTL)
			if _, err := pipe.Exec(ctx); err != nil {
				response.Report(c, err)
				return
			}
			middleware.RecordAudit(a.DB, c, "login", 401, "登录失败")
			response.Fail(c, 401, "账号或密码错误")
			return
		}
		token, refresh := rand.Text(), rand.Text()
		now := time.Now().UnixMilli()
		session := models.Session{ID: rand.Text(), UserID: user.ID, AccessHash: admin.TokenHash(token), RefreshHash: admin.TokenHash(refresh), ExpiresAt: time.Now().Add(a.Options.AccessTTL).UnixMilli(), RefreshExpiresAt: time.Now().Add(a.Options.RefreshTTL).UnixMilli(), IP: c.ClientIP(), UserAgent: c.Request.UserAgent()}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&session).Error; err != nil {
				return err
			}
			return tx.Model(&user).Update("login_time", now).Error
		}); err != nil {
			response.Report(c, err)
			return
		}
		if err := a.Redis.Del(ctx, limit).Err(); err != nil {
			response.Report(c, err)
			return
		}
		c.Set("dawn.actor", &admin.Actor{User: user})
		middleware.RecordAudit(a.DB, c, "login", 200, "登录成功")
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("dawn_session", token, int(a.Options.AccessTTL.Seconds()), a.Options.AdminPrefix, "", c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https", true)
		response.Respond(c, gin.H{"access_token": token, "refresh_token": refresh, "token_type": "Bearer", "expires_in": int(a.Options.AccessTTL.Seconds())})
	})
	g.POST("/auth/refresh", func(c *gin.Context) {
		var input struct {
			Token string `json:"refresh_token" binding:"required"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "缺少刷新凭证")
			return
		}
		var old models.Session
		if err := a.DB.Where("refresh_hash = ? AND refresh_expires_at > ?", admin.TokenHash(input.Token), time.Now().UnixMilli()).First(&old).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.Fail(c, 401, "刷新凭证已失效")
			} else {
				response.Report(c, err)
			}
			return
		}
		var user models.User
		if err := a.DB.Where("status = ?", models.Enabled).First(&user, old.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.Fail(c, 401, "账号已失效")
			} else {
				response.Report(c, err)
			}
			return
		}
		token, refresh := rand.Text(), rand.Text()
		result := a.DB.Model(&models.Session{}).Where("id = ? AND refresh_hash = ?", old.ID, admin.TokenHash(input.Token)).Updates(map[string]any{"access_hash": admin.TokenHash(token), "refresh_hash": admin.TokenHash(refresh), "expires_at": time.Now().Add(a.Options.AccessTTL).UnixMilli(), "refresh_expires_at": time.Now().Add(a.Options.RefreshTTL).UnixMilli()})
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			response.Fail(c, 401, "刷新凭证已经使用")
			return
		}
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie("dawn_session", token, int(a.Options.AccessTTL.Seconds()), a.Options.AdminPrefix, "", c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https", true)
		response.Respond(c, gin.H{"access_token": token, "refresh_token": refresh, "expires_in": int(a.Options.AccessTTL.Seconds())})
	})
	g.POST("/auth/logout", middleware.Require(a.App, ""), func(c *gin.Context) {
		if err := a.DB.Delete(&models.Session{}, "id = ?", middleware.Actor(c).SessionID).Error; err != nil {
			response.Report(c, err)
			return
		}
		c.SetCookie("dawn_session", "", -1, a.Options.AdminPrefix, "", false, true)
		response.Respond(c, nil)
	})
	g.GET("/auth/me", middleware.Require(a.App, ""), func(c *gin.Context) {
		u := middleware.Actor(c).User
		roleIDs, postIDs := []uint{}, []uint{}
		roles, buttons := []string{}, []string{}
		for _, r := range u.Roles {
			roleIDs = append(roleIDs, r.ID)
			roles = append(roles, r.Code)
		}
		for _, p := range u.Posts {
			postIDs = append(postIDs, p.ID)
		}
		for key := range a.PermissionDefinitions {
			if middleware.Actor(c).Can(key) {
				buttons = append(buttons, key)
			}
		}
		var customKeys []string
		if err := a.DB.Model(&models.Menu{}).Where("type <> ?", models.MenuDirectory).Pluck("key", &customKeys).Error; err != nil {
			response.Report(c, err)
			return
		}
		for _, key := range customKeys {
			if middleware.Actor(c).Can(key) {
				buttons = append(buttons, key)
			}
		}
		var department models.Department
		if err := a.DB.Select("id", "name").Where("id = ?", u.DepartmentID).Find(&department).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"id": u.ID, "username": u.Username, "realname": u.Realname, "email": u.Email, "phone": u.Phone, "avatar": u.Avatar, "gender": u.Gender, "remark": u.Remark, "roles": roles, "buttons": buttons, "role_ids": roleIDs, "post_ids": postIDs, "dept_id": u.DepartmentID, "department": gin.H{"id": department.ID, "name": department.Name}, "is_super": u.Super, "dashboard": "/dashboard/console"})
	})
	g.PUT("/auth/profile", middleware.Require(a.App, ""), func(c *gin.Context) {
		var input struct {
			Realname *string `json:"realname" binding:"omitempty,max=100"`
			Email    *string `json:"email" binding:"omitempty,max=180,email|eq="`
			Phone    *string `json:"phone" binding:"omitempty,max=40"`
			Avatar   *string `json:"avatar" binding:"omitempty,max=500"`
			Gender   *string `json:"gender" binding:"omitempty,oneof=0 1 2"`
			Remark   *string `json:"remark" binding:"omitempty,max=2000"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "资料格式错误")
			return
		}
		if err := a.DB.Model(&models.User{}).Where("id = ?", middleware.Actor(c).User.ID).Updates(input).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.PUT("/auth/password", middleware.Require(a.App, ""), func(c *gin.Context) {
		var input struct {
			Old      string `json:"old_password" binding:"required"`
			Password string `json:"password" binding:"required,min=6,max=72"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "新密码长度须为6至72字符")
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(middleware.Actor(c).User.Password), []byte(input.Old)) != nil {
			response.Fail(c, 422, "原密码错误")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			response.Report(c, err)
			return
		}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.User{}).Where("id = ?", middleware.Actor(c).User.ID).Update("password", string(hash)).Error; err != nil {
				return err
			}
			return tx.Delete(&models.Session{}, "user_id = ?", middleware.Actor(c).User.ID).Error
		}); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.GET("/auth/options", middleware.Require(a.App, ""), func(c *gin.Context) {
		options := map[string][]gin.H{
			"data_status": []gin.H{{"label": "正常", "value": 1, "color": "#13ce66"}, {"label": "停用", "value": 2, "color": "#ff4949"}},
			"menu_type":   []gin.H{{"label": "目录", "value": models.MenuDirectory, "color": "#909399"}, {"label": "菜单", "value": models.MenuPage, "color": "#409eff"}, {"label": "按钮", "value": models.MenuAction, "color": "#f56c6c"}, {"label": "外链", "value": models.MenuLink, "color": "#67c23a"}},
			"yes_or_no":   []gin.H{{"label": "是", "value": 1}, {"label": "否", "value": 2}},
			"gender":      []gin.H{{"label": "未知", "value": "0"}, {"label": "男", "value": "1"}, {"label": "女", "value": "2"}},
			"dashboard":   []gin.H{{"label": "工作台", "value": "/dashboard/console"}},
		}
		for _, choices := range options {
			for _, choice := range choices {
				choice["label"] = i18n.Text(c.GetString("admin.locale"), choice["label"].(string))
			}
		}
		response.Respond(c, options)
	})
}
