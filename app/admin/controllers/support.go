package controllers

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/encryption"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *Controller) supportApps(c *gin.Context) *gorm.DB {
	return a.DB.WithContext(c.Request.Context()).Model(&models.SupportApp{}).Scopes(middleware.Actor(c).Scope("created_by", ""))
}

func (a *Controller) SupportRoutes(g *route.Registrar) {
	handlers := ResourceHandlers{}
	g.Put("/support-actions/{id}", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			Approved bool `json:"approved"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.DecideAction(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, input.Approved); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})

	g.Post("/telegram-bindings", middleware.Require(a.App, ""), func(c *gin.Context) {
		code, err := a.Customer.CreateTelegramBinding(c.Request.Context(), middleware.Actor(c).User.ID)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, gin.H{"code": code}, a.Options.AdminPrefix+"/telegram-bindings/"+code)
	})
	g.Get("/telegram-bindings/{code}", middleware.Require(a.App, ""), func(c *gin.Context) {
		binding, err := a.Customer.TelegramBinding(c.Request.Context(), middleware.Actor(c).User.ID, c.Param("code"))
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, binding)
	})
	g.Put("/telegram-binding", middleware.Require(a.App, ""), func(c *gin.Context) {
		var input struct {
			Code string `json:"code"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.ConfirmTelegramBinding(c.Request.Context(), middleware.Actor(c).User.ID, input.Code); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.Get("/support-agents", middleware.Require(a.App, ""), func(c *gin.Context) {
		q := a.DB.WithContext(c.Request.Context()).Model(&models.AdminUser{}).Where("status = ?", models.Enabled).Where("super = ? OR id IN (SELECT ur.user_id FROM admin_user_roles AS ur JOIN admin_roles AS r ON r.id = ur.role_id AND r.status = 1 AND r.deleted_at = 0 JOIN admin_role_permissions AS p ON p.role_id = ur.role_id WHERE p.key = ?)", true, "dawn.support.reply")
		if value := c.Query("app_id"); value != "" {
			id, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				response.Report(c, customer.ErrInvalid)
				return
			}
			_, cfg, _, err := a.Customer.App(c.Request.Context(), uint(id))
			if err != nil {
				response.Report(c, err)
				return
			}
			if a.Customer.AgentAccess(c.Request.Context(), uint(id), middleware.Actor(c).User.ID, "view") != nil {
				response.Report(c, customer.ErrForbidden)
				return
			}
			q = q.Where("id IN ?", cfg.AgentIDs)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Realname string `json:"realname"`
			Avatar   string `json:"avatar"`
		}{}
		if err := q.Select("id", "username", "realname", "avatar").Order("id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	})

	handlers.Index = func(c *gin.Context) {
		q := a.supportApps(c)
		if name := c.Query("name"); name != "" {
			q = q.Where("name LIKE ?", "%"+name+"%")
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		rows := []models.SupportApp{}
		if err := q.Order(response.Order(c, "id", "name", "created_at")).Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
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
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, app)
	}
	g.Get("/support-apps/{id}/secrets", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		var secrets customer.Secrets
		if app.Secrets != "" {
			plain, err := encryption.DecryptString(c.Request.Context(), app.Secrets)
			if err != nil {
				response.Report(c, err)
				return
			}
			if err = json.Unmarshal([]byte(plain), &secrets); err != nil {
				response.Report(c, err)
				return
			}
		}
		c.Header("Cache-Control", "no-store")
		response.Respond(c, gin.H{"identity_secret": secrets.IdentitySecret, "telegram_token": secrets.TelegramToken})
	})
	g.Get("/support-apps/{id}/telegram-groups", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		groups := []customer.TelegramGroup{}
		if app.TelegramBotID != nil {
			values, err := a.Customer.Redis.HVals(c.Request.Context(), a.Customer.Key(constants.TempTelegramGroups, app.ID, *app.TelegramBotID)).Result()
			if err != nil {
				response.Report(c, err)
				return
			}
			for _, value := range values {
				var group customer.TelegramGroup
				if err = json.Unmarshal([]byte(value), &group); err != nil {
					response.Report(c, err)
					return
				}
				groups = append(groups, group)
			}
		}
		slices.SortFunc(groups, func(a, b customer.TelegramGroup) int { return strings.Compare(a.Title, b.Title) })
		response.Respond(c, groups)
	})
	g.Post("/support-apps/{id}/model-tests", middleware.Require(a.App, "dawn.support_apps.update"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var app models.SupportApp
		if err := a.supportApps(c).First(&app, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
		defer cancel()
		result, err := a.Customer.TestModel(ctx, app.ID)
		if err != nil {
			response.Fail(c, 502, "模型连接失败：%s", err.Error())
			return
		}
		response.Created(c, result, c.Request.URL.Path)
	})
	write := func(c *gin.Context) {
		app := models.SupportApp{Status: models.Enabled}
		if c.Request.Method != "POST" {
			id := response.ID(c)
			if id == 0 {
				return
			}
			if err := a.supportApps(c).First(&app, id).Error; err != nil {
				response.Report(c, err)
				return
			}
		}
		previous := app.Revision
		var previousCfg customer.Config
		_ = json.Unmarshal(app.Config, &previousCfg)
		var input map[string]json.RawMessage
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		secrets := customer.Secrets{}
		keySubmitted := false
		_, secretsChanged := input["secrets"]
		if raw, ok := input["secrets"]; ok {
			if string(raw) == "null" {
				response.Report(c, customer.ErrInvalid)
				return
			}
			if app.Secrets != "" {
				plain, err := encryption.DecryptString(c.Request.Context(), app.Secrets)
				if err != nil {
					response.Report(c, err)
					return
				}
				if json.Unmarshal([]byte(plain), &secrets) != nil {
					response.Report(c, customer.ErrInvalid)
					return
				}
			}
			oldKey := secrets.APIKey
			if json.Unmarshal(raw, &secrets) != nil {
				response.Report(c, customer.ErrInvalid)
				return
			}
			var changes map[string]json.RawMessage
			if json.Unmarshal(raw, &changes) != nil {
				response.Report(c, customer.ErrInvalid)
				return
			}
			for key, value := range changes {
				if !slices.Contains([]string{"api_key", "embedding_api_key", "identity_secret", "telegram_token", "tool_headers"}, key) || (string(value) == "null" && key != "tool_headers") {
					response.Report(c, customer.ErrInvalid)
					return
				}
			}
			_, keySubmitted = changes["api_key"]
			keySubmitted = keySubmitted && secrets.APIKey != ""
			if secrets.APIKey == "" {
				secrets.APIKey = oldKey
			}
			delete(input, "secrets")
		}
		for key := range input {
			if (key != "name" && key != "avatar" && key != "status" && key != "config" && key != "revision") || string(input[key]) == "null" {
				response.Report(c, customer.ErrInvalid)
				return
			}
		}
		if c.Request.Method == "PUT" {
			app.Name = ""
			app.Avatar = ""
			app.Status = models.Enabled
			app.Config = customer.Raw(customer.Config{})
		}
		if json.Unmarshal(customer.Raw(input), &app) != nil || strings.TrimSpace(app.Name) == "" || utf8.RuneCountInString(app.Name) > 100 || app.Status != 1 && app.Status != 2 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		var cfg customer.Config
		if len(app.Config) > 0 && json.Unmarshal(app.Config, &cfg) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		cfg.ActiveIndex = previousCfg.ActiveIndex
		cfg.IndexProfile = previousCfg.IndexProfile
		app.Config = customer.Raw(cfg)
		if err := customer.ValidateConfig(cfg); err != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if app.ID != 0 {
			if _, ok := input["revision"]; !ok || app.Revision != previous {
				response.Report(c, customer.ErrConflict)
				return
			}
		}
		if keySubmitted {
			ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
			_, err := a.Customer.CheckModel(ctx, cfg, secrets.APIKey)
			cancel()
			if err != nil {
				message := i18n.Text(c.GetString("admin.locale"), "模型密钥验证失败：%s", err.Error())
				c.AbortWithStatusJSON(422, gin.H{"message": message, "errors": gin.H{"secrets.api_key": message}})
				return
			}
		}
		if app.ID == 0 {
			app.CreatedBy = middleware.Actor(c).User.ID
			if err := a.Customer.CreateApp(c.Request.Context(), &app, secrets); err != nil {
				response.Report(c, err)
				return
			}
			response.Created(c, app, a.Options.AdminPrefix+"/support-apps/"+strconv.FormatUint(uint64(app.ID), 10))
			return
		}
		updates := map[string]any{"name": app.Name, "avatar": app.Avatar, "status": app.Status, "config": app.Config, "revision": previous + 1}
		if secretsChanged {
			value, err := encryption.EncryptString(c.Request.Context(), string(customer.Raw(secrets)))
			if err != nil {
				response.Report(c, err)
				return
			}
			updates["secrets"] = value
			var botID *int64
			if secrets.TelegramToken != "" {
				prefix, _, ok := strings.Cut(secrets.TelegramToken, ":")
				id, e := strconv.ParseInt(prefix, 10, 64)
				if !ok || e != nil || id <= 0 {
					response.Report(c, customer.ErrInvalid)
					return
				}
				botID = &id
			}
			updates["telegram_bot_id"] = botID
		}
		result := a.supportApps(c).Where("id = ? AND revision = ?", app.ID, previous).Updates(updates)
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected != 1 {
			response.Report(c, customer.ErrConflict)
			return
		}
		app.Revision++
		response.Respond(c, app)
	}
	handlers.Store, handlers.Update = write, write
	handlers.Destroy = func(c *gin.Context) {
		ids := response.IDs(c)
		if c.IsAborted() {
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&models.SupportApp{}).Scopes(middleware.Actor(c).Scope("created_by", "")).Where("id IN ?", ids)
			var total int64
			if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
				return err
			}
			if total != int64(len(ids)) {
				return gorm.ErrRecordNotFound
			}
			return q.Delete(&models.SupportApp{}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	}

	g.Get("/support-capabilities", middleware.Require(a.App, ""), func(c *gin.Context) {
		actor := middleware.Actor(c)
		response.Respond(c, gin.H{"view": actor.Can("dawn.support.view"), "reply": actor.Can("dawn.support.reply"), "manage": actor.Can("dawn.support_apps.list")})
	})
	g.Get("/support-inbox", middleware.Require(a.App, "dawn.support.view"), func(c *gin.Context) {
		ids, err := a.Customer.AppIDs(c.Request.Context(), middleware.Actor(c).User.ID, "view")
		if err != nil {
			response.Report(c, err)
			return
		}
		q := a.DB.WithContext(c.Request.Context()).Model(&models.ChatConversation{}).Where("chat_conversations.type = ? AND chat_conversations.app_id IN ?", "support", ids)
		if status := c.Query("status"); status != "" {
			q = q.Where("chat_conversations.status = ?", status)
		}
		if c.Query("mine") == "1" {
			q = q.Where("assigned_admin_id = ?", middleware.Actor(c).User.ID)
		}
		if appID := c.Query("app_id"); appID != "" {
			q = q.Where("chat_conversations.app_id = ?", appID)
		}
		var total int64
		if err = q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		type row struct {
			models.ChatConversation
			AppName           string          `json:"app_name"`
			Attributes        json.RawMessage `json:"-"`
			Nickname          string          `json:"nickname"`
			UniqueID          string          `json:"unique_id"`
			Avatar            string          `json:"avatar"`
			LastReadMessageID uint            `json:"last_read_message_id"`
			Unread            int64           `json:"unread"`
			Content           string          `json:"content"`
		}
		rows := []row{}
		subject := strconv.FormatUint(uint64(middleware.Actor(c).User.ID), 10)
		err = q.Select("chat_conversations.*, app.name AS app_name, u.nickname, u.unique_id, u.avatar, traits.attributes, COALESCE(member.last_read_message_id,0) AS last_read_message_id, last.content, CASE WHEN chat_conversations.status IN ('waiting','human') THEN (SELECT COUNT(*) FROM chat_messages AS msg WHERE msg.conversation_id = chat_conversations.id AND msg.id > COALESCE(member.last_read_message_id,0) AND msg.visibility = 'public' AND msg.sender_realm LIKE 'support:%') ELSE 0 END AS unread").Joins("LEFT JOIN support_apps AS app ON app.id = chat_conversations.app_id").Joins("LEFT JOIN users AS u ON u.id = chat_conversations.user_id").Joins("LEFT JOIN user_traits AS traits ON traits.user_id = chat_conversations.user_id AND traits.app_id = chat_conversations.app_id").Joins("LEFT JOIN chat_members AS member ON member.conversation_id = chat_conversations.id AND member.realm = 'admin' AND member.subject = ?", subject).Joins("LEFT JOIN chat_messages AS last ON last.id = chat_conversations.last_message_id").Order("chat_conversations.last_message_id DESC, chat_conversations.id DESC").Offset((page - 1) * size).Limit(size).Scan(&rows).Error
		if err != nil {
			response.Report(c, err)
			return
		}
		for i := range rows {
			if err = a.Customer.FillVisitorPresence(c.Request.Context(), &rows[i].ChatConversation, rows[i].UniqueID); err != nil {
				response.Report(c, err)
				return
			}
			if rows[i].Nickname != "" {
				continue
			}
			var attributes map[string]any
			_ = json.Unmarshal(rows[i].Attributes, &attributes)
			for _, key := range []string{"nick", "nickname", "user", "username"} {
				if value, ok := attributes[key].(string); ok && value != "" {
					rows[i].Nickname = value
					break
				}
			}
		}
		var unread int64
		err = a.DB.WithContext(c.Request.Context()).Table("chat_messages AS msg").Joins("JOIN chat_conversations AS conv ON conv.id = msg.conversation_id AND conv.type = 'support'").Joins("LEFT JOIN chat_members AS member ON member.conversation_id = conv.id AND member.realm = 'admin' AND member.subject = ?", subject).Where("conv.app_id IN ? AND conv.status IN ? AND msg.visibility = 'public' AND msg.sender_realm LIKE 'support:%' AND msg.id > COALESCE(member.last_read_message_id,0)", ids, []string{"waiting", "human"}).Count(&unread).Error
		if err != nil {
			response.Report(c, err)
			return
		}
		var apps []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		}
		if err = a.DB.WithContext(c.Request.Context()).Model(&models.SupportApp{}).Select("id", "name").Where("id IN ?", ids).Order("id").Scan(&apps).Error; err != nil {
			response.Report(c, err)
			return
		}
		now := time.Now().UTC()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		activity := a.DB.WithContext(c.Request.Context()).Table("chat_messages AS msg").Joins("JOIN chat_conversations AS conv ON conv.id = msg.conversation_id").Where("conv.type = 'support' AND conv.app_id IN ? AND msg.created_at >= ? AND msg.created_at < ?", ids, start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli())
		var replies int64
		if err = activity.Session(&gorm.Session{}).Where("msg.sender_realm = 'admin' AND msg.sender_subject = ? AND msg.type = 'text' AND msg.visibility = 'public' AND msg.status = 'completed'", subject).Count(&replies).Error; err != nil {
			response.Report(c, err)
			return
		}
		var receptions []struct {
			UserID  uint
			Payload json.RawMessage
		}
		if err = activity.Session(&gorm.Session{}).Select("conv.user_id, msg.payload").Where("msg.type = 'event' AND msg.content IN ?", []string{"claim", "transfer"}).Scan(&receptions).Error; err != nil {
			response.Report(c, err)
			return
		}
		customers := map[uint]bool{}
		for _, reception := range receptions {
			var payload struct {
				AssignedAdminID uint `json:"assigned_admin_id"`
			}
			if json.Unmarshal(reception.Payload, &payload) == nil && payload.AssignedAdminID == middleware.Actor(c).User.ID {
				customers[reception.UserID] = true
			}
		}
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"page": page, "page_size": size, "total": total, "unread_total": unread, "apps": apps, "statistics": gin.H{"replies": replies, "customers": len(customers)}}})
	})
	g.Post("/support-presence/heartbeats", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		online, err := a.Customer.AgentHeartbeat(c.Request.Context(), middleware.Actor(c).User.ID)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"online": online})
	})
	g.Put("/support-presence", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		var input struct {
			Online bool `json:"online"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.SetPresence(c.Request.Context(), middleware.Actor(c).User.ID, input.Online); err != nil {
			response.Report(c, err)
			return
		}
		if input.Online {
			_ = a.Customer.AssignWaiting(c.Request.Context())
		}
		response.Respond(c, nil)
	})
	g.Get("/support-conversations/{id}", middleware.Require(a.App, "dawn.support.view"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		conv, err := a.Customer.Conversation(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, "view")
		if err != nil {
			response.Report(c, err)
			return
		}
		var user models.User
		var traits models.UserTraits
		var members []models.ChatMember
		if err = a.DB.WithContext(c.Request.Context()).First(&user, conv.UserID).Error; err != nil {
			response.Report(c, err)
			return
		}
		if err = a.DB.WithContext(c.Request.Context()).Where("app_id = ? AND user_id = ?", conv.AppID, conv.UserID).First(&traits).Error; err != nil {
			response.Report(c, err)
			return
		}
		if err = a.DB.WithContext(c.Request.Context()).Where("conversation_id = ?", id).Find(&members).Error; err != nil {
			response.Report(c, err)
			return
		}
		previews := []json.RawMessage{}
		for _, member := range members {
			if member.Realm != "admin" && len(member.Extra) > 0 {
				previews = append(previews, member.Extra)
			}
		}
		if err = a.Customer.FillVisitorPresence(c.Request.Context(), &conv, user.UniqueID); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"conversation": conv, "user": user, "traits": traits, "input_previews": previews})
	})
	g.Get("/support-conversations/{id}/messages", middleware.Require(a.App, "dawn.support.view"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		_, size := response.Page(c)
		before, _ := strconv.ParseUint(c.Query("before_id"), 10, 64)
		rows, more, err := a.Customer.History(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, uint(before), size)
		if err != nil {
			response.Report(c, err)
			return
		}
		var next uint
		if len(rows) > 0 {
			next = rows[len(rows)-1].ID
		}
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"has_more": more, "before_id": next}})
	})
	g.Post("/support-conversations/{id}/messages", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			RequestID  string `json:"request_id"`
			Content    string `json:"content"`
			Visibility string `json:"visibility"`
			Version    uint   `json:"version"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		message, err := a.Customer.Send(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, customer.MessageInput{Version: input.Version, RequestID: input.RequestID, Content: input.Content, Visibility: input.Visibility})
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, message, c.Request.URL.Path+"/"+strconv.FormatUint(uint64(message.ID), 10))
	})
	g.Put("/support-conversations/{id}/read-position", middleware.Require(a.App, "dawn.support.view"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			MessageID uint `json:"message_id"`
		}
		if c.ShouldBindJSON(&input) != nil || input.MessageID == 0 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.Read(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, input.MessageID); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.Put("/support-conversations/{id}/reception", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			Action  string `json:"action"`
			AdminID uint   `json:"admin_id"`
			Version uint   `json:"version"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		conv, err := a.Customer.Reception(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, input.AdminID, input.Version, input.Action)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, conv)
	})
	g.Post("/support-conversations/{id}/drafts", middleware.Require(a.App, "dawn.support.reply"), func(c *gin.Context) {
		id := response.ID(c)
		if id == 0 {
			return
		}
		conv, err := a.Customer.Conversation(c.Request.Context(), id, customer.Session{}, middleware.Actor(c).User.ID, "reply")
		if err != nil {
			response.Report(c, err)
			return
		}
		if conv.AssignedAdminID != middleware.Actor(c).User.ID || conv.Status != "human" {
			response.Report(c, customer.ErrConflict)
			return
		}
		var reply models.ChatMessage
		err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; e != nil {
				return e
			}
			if conv.Status != "human" || conv.AssignedAdminID != middleware.Actor(c).User.ID {
				return customer.ErrConflict
			}
			var trigger models.ChatMessage
			if e := tx.Where("conversation_id = ? AND sender_realm = ? AND type = ?", id, customer.Realm(conv.AppID), "text").Order("id DESC").First(&trigger).Error; e != nil {
				return e
			}
			var active models.AIRun
			found := tx.Where("conversation_id = ? AND kind = ? AND version = ? AND status IN ?", id, "draft", conv.Version, []string{"queued", "running"}).Order("id DESC").Limit(1).Find(&active)
			if found.Error != nil {
				return found.Error
			}
			if found.RowsAffected > 0 {
				return tx.First(&reply, active.ReplyID).Error
			}
			reply = models.ChatMessage{SenderRealm: "ai", SenderSubject: strconv.FormatUint(uint64(conv.AppID), 10), Status: "queued", Visibility: "draft", ReplyToID: trigger.ID}
			if e := a.Customer.Message(tx, &conv, &reply); e != nil {
				return e
			}
			return tx.Create(&models.AIRun{AppID: conv.AppID, ConversationID: id, MessageID: trigger.ID, ReplyID: reply.ID, Kind: "draft", Version: conv.Version, DeadlineAt: time.Now().Add(3 * time.Minute).UnixMilli()}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		a.WakeChat(c.Request.Context())
		response.Created(c, reply, c.Request.URL.Path)
	})

	RegisterResourceRoutes(a.App, g, "/support-apps", "dawn.support_apps", handlers)
}
