package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// chatMember applies the same membership boundary to history, sending and read receipts.
func (a *Controller) chatMember(c *gin.Context) bool {
	id := response.ID(c)
	if id == 0 {
		return false
	}
	var member models.ChatMember
	if err := a.DB.WithContext(c.Request.Context()).Where("conversation_id = ? AND realm = ? AND subject = ?", id, "admin", strconv.FormatUint(uint64(middleware.Actor(c).User.ID), 10)).First(&member).Error; err != nil {
		response.Report(c, err)
		return false
	}
	return true
}

func (a *Controller) ChatRoutes(g *route.Registrar) {
	identityColumn := "CAST(u.id AS TEXT)"
	if a.DB.Dialector.Name() == "mysql" {
		identityColumn = "CAST(u.id AS CHAR)"
	}
	g.Get("/chat-contacts", middleware.Require(a.App, ""), func(c *gin.Context) {
		db := a.DB.WithContext(c.Request.Context()).Model(&models.User{}).Where("id <> ? AND status = ?", middleware.Actor(c).User.ID, models.Enabled)
		if search := c.Query("search"); search != "" {
			db = db.Where("username LIKE ? OR realname LIKE ?", "%"+search+"%", "%"+search+"%")
		}
		page, size := response.Page(c)
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		rows := []struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Realname string `json:"realname"`
			Avatar   string `json:"avatar"`
		}{}
		if err := db.Select("id", "username", "realname", "avatar").Order("id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	})
	g.Post("/chat-conversations", middleware.Require(a.App, ""), func(c *gin.Context) {
		var input struct {
			UserID uint `json:"user_id" binding:"required"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "聊天参数格式错误")
			return
		}
		actor := middleware.Actor(c)
		if input.UserID == actor.User.ID {
			response.Fail(c, 422, "不能与自己聊天")
			return
		}
		var peer models.User
		if err := a.DB.WithContext(c.Request.Context()).Where("status = ?", models.Enabled).First(&peer, input.UserID).Error; err != nil {
			response.Report(c, err)
			return
		}
		conversation := models.ChatConversation{Type: "direct", Identify: fmt.Sprintf("direct:admin:%d:admin:%d", min(actor.User.ID, peer.ID), max(actor.User.ID, peer.ID))}
		created := false
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "identify"}}, DoNothing: true}).Create(&conversation)
			if result.Error != nil {
				return result.Error
			}
			created = result.RowsAffected > 0
			if err := tx.Where("identify = ?", conversation.Identify).First(&conversation).Error; err != nil {
				return err
			}
			members := []models.ChatMember{
				{ConversationID: conversation.ID, Realm: "admin", Subject: strconv.FormatUint(uint64(actor.User.ID), 10)},
				{ConversationID: conversation.ID, Realm: "admin", Subject: strconv.FormatUint(uint64(peer.ID), 10)},
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&members).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		if created {
			response.Created(c, conversation, fmt.Sprintf("%s/chat-conversations/%d", a.Options.AdminPrefix, conversation.ID))
		} else {
			response.Respond(c, conversation)
		}
	})
	g.Get("/chat-conversations", middleware.Require(a.App, ""), func(c *gin.Context) {
		subject := strconv.FormatUint(uint64(middleware.Actor(c).User.ID), 10)
		type row struct {
			models.ChatConversation
			PeerID                uint   `json:"peer_id"`
			Username              string `json:"username"`
			Realname              string `json:"realname"`
			Avatar                string `json:"avatar"`
			Content               string `json:"content"`
			Unread                int64  `json:"unread"`
			LastReadMessageID     uint   `json:"last_read_message_id"`
			PeerLastReadMessageID uint   `json:"peer_last_read_message_id"`
		}
		db := a.DB.WithContext(c.Request.Context()).Table("chat_conversations AS conv").
			Joins("JOIN chat_members AS mine ON mine.conversation_id = conv.id AND mine.realm = ? AND mine.subject = ?", "admin", subject).
			Joins("JOIN chat_members AS peer ON peer.conversation_id = conv.id AND peer.realm = ? AND peer.subject <> ?", "admin", subject).
			Joins("LEFT JOIN admin_users AS u ON peer.subject = "+identityColumn).
			Joins("LEFT JOIN chat_messages AS last ON last.id = conv.last_message_id").Where("conv.type = ?", "direct")
		page, size := response.Page(c)
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		rows := []row{}
		err := db.Select(`conv.*, u.id AS peer_id, u.username, u.realname, u.avatar, last.content, mine.last_read_message_id, peer.last_read_message_id AS peer_last_read_message_id,
   (SELECT COUNT(*) FROM chat_messages AS msg WHERE msg.conversation_id = conv.id AND msg.id > mine.last_read_message_id AND NOT (msg.sender_realm = 'admin' AND msg.sender_subject = ?) AND msg.status = 'completed') AS unread`, subject).
			Order("conv.last_message_id DESC, conv.id DESC").Offset((page - 1) * size).Limit(size).Scan(&rows).Error
		if err != nil {
			response.Report(c, err)
			return
		}
		var unread int64
		if err := a.DB.WithContext(c.Request.Context()).Model(&models.ChatMessage{}).Joins("JOIN chat_members AS mine ON mine.conversation_id = chat_messages.conversation_id AND mine.realm = ? AND mine.subject = ?", "admin", subject).Where("chat_messages.id > mine.last_read_message_id AND chat_messages.status = ? AND NOT (sender_realm = ? AND sender_subject = ?)", "completed", "admin", subject).Count(&unread).Error; err != nil {
			response.Report(c, err)
			return
		}
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"total": total, "page": page, "page_size": size, "unread_total": unread}})
	})
	g.Get("/chat-conversations/{id}/messages", middleware.Require(a.App, ""), func(c *gin.Context) {
		if !a.chatMember(c) {
			return
		}
		_, size := response.Page(c)
		db := a.DB.WithContext(c.Request.Context()).Where("conversation_id = ?", c.Param("id"))
		if before := c.Query("before_id"); before != "" {
			db = db.Where("id < ?", before)
		}
		rows := []models.ChatMessage{}
		if err := db.Order("id DESC").Limit(size + 1).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		more := len(rows) > size
		if more {
			rows = rows[:size]
		}
		var next uint
		if len(rows) > 0 {
			next = rows[len(rows)-1].ID
		}
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"has_more": more, "before_id": next}})
	})
	g.Get("/chat-conversations/{id}/messages/{message_id}", middleware.Require(a.App, ""), func(c *gin.Context) {
		if !a.chatMember(c) {
			return
		}
		messageID, err := strconv.ParseUint(c.Param("message_id"), 10, 64)
		if err != nil || messageID == 0 {
			response.Fail(c, 422, "无效的记录ID")
			return
		}
		var message models.ChatMessage
		if err := a.DB.WithContext(c.Request.Context()).Where("conversation_id = ?", c.Param("id")).First(&message, messageID).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, message)
	})
	g.Post("/chat-conversations/{id}/messages", middleware.Require(a.App, ""), func(c *gin.Context) {
		if !a.chatMember(c) {
			return
		}
		var input struct {
			RequestID string `json:"request_id" binding:"required,uuid"`
			Content   string `json:"content" binding:"required,max=4000"`
		}
		if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Content) == "" {
			response.Fail(c, 422, "消息内容格式错误")
			return
		}
		actor := middleware.Actor(c)
		subject := strconv.FormatUint(uint64(actor.User.ID), 10)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		message := models.ChatMessage{ConversationID: uint(id), SenderRealm: "admin", SenderSubject: subject, RequestID: input.RequestID, Type: "text", Status: "completed", Content: input.Content}
		conflict, disabled, limited := false, false, false
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			// Serialize each sender across conversations before checking the per-minute limit.
			var sender models.User
			if err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&sender, actor.User.ID).Error; err != nil {
				return err
			}
			var conversation models.ChatConversation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conversation, id).Error; err != nil {
				return err
			}
			var existing models.ChatMessage
			result := tx.Where("sender_realm = ? AND sender_subject = ? AND request_id = ?", "admin", subject, input.RequestID).Limit(1).Find(&existing)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				conflict = existing.ConversationID != uint(id) || existing.Content != input.Content
				message = existing
				return nil
			}
			// Disabled/deleted peers cannot receive new direct messages.
			var count int64
			if err := tx.Table("chat_members AS m").Joins("JOIN admin_users AS u ON m.subject = "+identityColumn).Where("m.conversation_id = ? AND m.realm = ? AND u.status = ? AND u.deleted_at = 0", id, "admin", models.Enabled).Count(&count).Error; err != nil {
				return err
			}
			disabled = conversation.Type != "direct" || count != 2
			if disabled {
				return nil
			}
			if err := tx.Model(&models.ChatMessage{}).Where("sender_realm = ? AND sender_subject = ? AND created_at > ?", "admin", subject, time.Now().Add(-time.Minute).UnixMilli()).Count(&count).Error; err != nil {
				return err
			}
			limited = count >= 60
			if limited {
				return nil
			}
			if err := tx.Create(&message).Error; err != nil {
				return err
			}
			if err := tx.Model(&conversation).Updates(map[string]any{"last_message_id": message.ID, "last_message_at": message.CreatedAt}).Error; err != nil {
				return err
			}
			data, err := json.Marshal(message)
			if err != nil {
				return err
			}
			return tx.Create(&models.ChatEvent{ConversationID: uint(id), Type: "chat.message.created", Data: data}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		if conflict {
			response.Fail(c, 409, "请求编号已使用")
			return
		}
		if disabled {
			response.Fail(c, 403, "当前会话不能发送消息")
			return
		}
		if limited {
			response.Fail(c, http.StatusTooManyRequests, "消息发送过于频繁")
			return
		}
		a.WakeChat(c.Request.Context())
		response.Created(c, message, fmt.Sprintf("%s/chat-conversations/%d/messages/%d", a.Options.AdminPrefix, id, message.ID))
	})
	g.Put("/chat-conversations/{id}/read-position", middleware.Require(a.App, ""), func(c *gin.Context) {
		if !a.chatMember(c) {
			return
		}
		var input struct {
			MessageID uint `json:"message_id" binding:"required"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "聊天参数格式错误")
			return
		}
		subject := strconv.FormatUint(uint64(middleware.Actor(c).User.ID), 10)
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var message models.ChatMessage
			if err := tx.Where("conversation_id = ? AND status = ?", c.Param("id"), "completed").First(&message, input.MessageID).Error; err != nil {
				return err
			}
			result := tx.Model(&models.ChatMember{}).Where("conversation_id = ? AND realm = ? AND subject = ? AND last_read_message_id < ?", message.ConversationID, "admin", subject, message.ID).Update("last_read_message_id", message.ID)
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			data, err := json.Marshal(gin.H{"conversation_id": message.ConversationID, "realm": "admin", "subject": subject, "message_id": message.ID})
			if err != nil {
				return err
			}
			return tx.Create(&models.ChatEvent{ConversationID: message.ConversationID, Type: "chat.read.updated", Data: data}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		a.WakeChat(c.Request.Context())
		response.Respond(c, nil)
	})
}
