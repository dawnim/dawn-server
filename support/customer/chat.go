package customer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (m *Manager) Conversation(ctx context.Context, id uint, session Session, adminID uint, action string) (models.ChatConversation, error) {
	var conv models.ChatConversation
	var err error
	if err := m.DB.WithContext(ctx).Where("type = ?", "support").First(&conv, id).Error; err != nil {
		return conv, err
	}
	conv.SupportSession, err = m.CurrentSession(ctx, id)
	if err != nil {
		return conv, err
	}
	if adminID != 0 {
		return conv, m.AgentAccess(ctx, conv.AppID, adminID, action)
	}
	if session.UserID != conv.UserID || session.AppID != conv.AppID || session.ID == "" {
		return conv, ErrForbidden
	}
	var app models.SupportApp
	if err := m.DB.WithContext(ctx).Where("id = ? AND status = ?", conv.AppID, models.Enabled).First(&app).Error; err != nil {
		return conv, ErrForbidden
	}
	return conv, nil
}

// Event 与状态写入同一事务，提交后通过Wake通知现有聊天投递器。
func (m *Manager) Event(tx *gorm.DB, conversationID uint, kind string, value any) error {
	return tx.Create(&models.ChatEvent{ConversationID: conversationID, Type: kind, Data: Raw(value)}).Error
}

func (m *Manager) Message(tx *gorm.DB, conv *models.ChatConversation, message *models.ChatMessage) error {
	message.ConversationID = conv.ID
	if message.RequestID == "" {
		message.RequestID = rand.Text()
	}
	if message.Status == "" {
		message.Status = "completed"
	}
	if message.Type == "" {
		message.Type = "text"
	}
	if message.Visibility == "" {
		message.Visibility = "public"
	}
	if err := tx.Create(message).Error; err != nil {
		return err
	}
	if message.Visibility == "public" {
		if err := tx.Model(conv).Updates(map[string]any{"last_message_id": message.ID, "last_message_at": message.CreatedAt}).Error; err != nil {
			return err
		}
	}
	return m.Event(tx, conv.ID, "chat.message.created", message)
}

type MessageInput struct {
	RequestID  string `json:"request_id"`
	Content    string `json:"content"`
	Visibility string `json:"visibility"`
	Version    uint   `json:"version"`
	externalID string
}

func (m *Manager) Send(ctx context.Context, id uint, session Session, adminID uint, input MessageInput) (models.ChatMessage, error) {
	requestID, content, visibility := input.RequestID, input.Content, input.Visibility
	var message models.ChatMessage
	if len(content) > 16000 || strings.TrimSpace(content) == "" || len(requestID) < 16 || len(requestID) > 64 {
		return message, ErrInvalid
	}
	if visibility != "" && visibility != "public" && visibility != "internal" {
		return message, ErrInvalid
	}
	conv, err := m.Conversation(ctx, id, session, adminID, "reply")
	if err != nil {
		return message, err
	}
	_, cfg, _, err := m.App(ctx, conv.AppID)
	if err != nil {
		return message, err
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; err != nil {
			return err
		}
		realm, subject := Realm(conv.AppID), session.User.UniqueID
		if adminID != 0 {
			realm = "admin"
			subject = strconv.FormatUint(uint64(adminID), 10)
		} else {
			visibility = "public"
		}
		found := tx.Where("sender_realm = ? AND sender_subject = ? AND request_id = ?", realm, subject, requestID).Limit(1).Find(&message)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if message.ConversationID != id || message.Content != content || message.Visibility != visibility && visibility != "" {
				return ErrConflict
			}
			return nil
		}
		if adminID != 0 && (conv.Status != "human" || conv.AssignedAdminID != adminID || conv.Version != input.Version) {
			return ErrConflict
		}
		var count int64
		if err := tx.Model(&models.ChatMessage{}).Where("sender_realm = ? AND sender_subject = ? AND created_at > ?", realm, subject, time.Now().Add(-time.Minute).UnixMilli()).Count(&count).Error; err != nil {
			return err
		}
		if count >= 60 {
			return ErrLimited
		}
		if err := m.Cancel(tx, &conv, false, "收到新消息"); err != nil {
			return err
		}
		if conv.Status == "closed" {
			conv.Status = "ai"
			conv.AssignedAdminID = 0
			if !cfg.AIEnabled {
				conv.Status = "waiting"
			}
			conv.Round++
			if err := tx.Model(&conv).Updates(map[string]any{"status": conv.Status, "round": conv.Round, "assigned_admin_id": 0}).Error; err != nil {
				return err
			}
		}
		if conv.Status == "waiting" {
			row := models.SupportSession{ConversationID: id, Round: conv.Round, Status: "waiting"}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
				return e
			}
		}
		if adminID == 0 {
			if e := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ? AND status IN ?", id, conv.Round, []string{"active", "waiting", "closing_pending"}).Updates(map[string]any{"offline_since": 0, "last_online_at": time.Now().UnixMilli()}).Error; e != nil {
				return e
			}
		}
		if visibility != "internal" {
			if e := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ? AND status = ?", id, conv.Round, "closing_pending").Updates(map[string]any{"status": "active", "closing_requested_at": 0, "version": gorm.Expr("version + 1")}).Error; e != nil {
				return e
			}
		}
		if e := m.receptionEvent(tx, &conv); e != nil {
			return e
		}
		message = models.ChatMessage{SenderRealm: realm, SenderSubject: subject, RequestID: requestID, Content: content, Visibility: visibility, Payload: Raw(map[string]any{"verified": session.Verified, "user_type": session.User.Type})}
		if input.externalID != "" {
			message.ExternalID = &input.externalID
		}
		if err := m.Message(tx, &conv, &message); err != nil {
			return err
		}
		var member models.ChatMember
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND realm = ? AND subject = ?", id, realm, subject).First(&member).Error; e != nil {
			return e
		}
		if err := tx.Model(&models.ChatMember{}).Where("conversation_id = ? AND realm = ? AND subject = ? AND extra IS NOT NULL", id, realm, subject).Update("extra", nil).Error; err != nil {
			return err
		}
		if adminID == 0 && conv.Status == "ai" && cfg.AIEnabled {
			reply := models.ChatMessage{SenderRealm: "ai", SenderSubject: strconv.FormatUint(uint64(conv.AppID), 10), Status: "queued", ReplyToID: message.ID}
			if err := m.Message(tx, &conv, &reply); err != nil {
				return err
			}
			return tx.Create(&models.AIRun{AppID: conv.AppID, ConversationID: id, MessageID: message.ID, ReplyID: reply.ID, Kind: "reply", Version: conv.Version, DeadlineAt: time.Now().Add(3 * time.Minute).UnixMilli()}).Error
		}
		return nil
	})
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return message, err
}

var ErrLimited = fmt.Errorf("消息发送过于频繁")

// Cancel 使旧执行和迟到片段失效；仅转人工且正在生成时保留内部草稿。
func (m *Manager) Cancel(tx *gorm.DB, conv *models.ChatConversation, draft bool, reason string) error {
	var runs []models.AIRun
	if err := tx.Where("conversation_id = ? AND status IN ?", conv.ID, []string{"queued", "running", "waiting"}).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		var snapshot struct {
			Content string `json:"content"`
		}
		if run.ReplyID != 0 {
			value, _ := m.Redis.Get(tx.Statement.Context, m.Key(constants.TempAIStream, run.ReplyID)).Bytes()
			_ = json.Unmarshal(value, &snapshot)
		}
		if draft && run.Status == "running" && run.Kind == "reply" && snapshot.Content != "" {
			message := models.ChatMessage{SenderRealm: "ai", SenderSubject: strconv.FormatUint(uint64(conv.AppID), 10), Content: snapshot.Content, Visibility: "draft", ReplyToID: run.MessageID}
			if err := m.Message(tx, conv, &message); err != nil {
				return err
			}
		}
		if err := tx.Model(&models.AIRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": "cancelled", "error": reason}).Error; err != nil {
			return err
		}
		if run.ReplyID == 0 {
			continue
		}
		var reply models.ChatMessage
		if err := tx.First(&reply, run.ReplyID).Error; err != nil {
			return err
		}
		reply.Status = "cancelled"
		reply.Payload = Raw(map[string]any{"error": reason})
		if snapshot.Content != "" {
			reply.Content = snapshot.Content
		}
		if err := tx.Model(&reply).Select("status", "content", "payload").Updates(&reply).Error; err != nil {
			return err
		}
		if err := m.Event(tx, conv.ID, "chat.message.updated", reply); err != nil {
			return err
		}
	}
	conv.Version++
	return tx.Model(conv).Update("version", conv.Version).Error
}

func (m *Manager) Reception(ctx context.Context, id uint, session Session, adminID, assignee uint, version uint, action string) (models.ChatConversation, error) {
	conv, err := m.Conversation(ctx, id, session, adminID, "reply")
	if err != nil {
		return conv, err
	}
	_, cfg, _, err := m.App(ctx, conv.AppID)
	if err != nil {
		return conv, err
	}
	if action == "claim" {
		assignee = adminID
	}
	if action == "claim" || action == "transfer" {
		if err = m.AgentAccess(ctx, conv.AppID, assignee, "reply"); err != nil {
			return conv, err
		}
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; err != nil {
			return err
		}
		if action == "human" {
			if conv.Status == "waiting" || conv.Status == "human" {
				return nil
			}
			if err := m.Cancel(tx, &conv, true, "已转人工接待"); err != nil {
				return err
			}
			if conv.Status == "closed" || (conv.SupportSession != nil && conv.SupportSession.Round == conv.Round && (conv.SupportSession.Status == "closed" || conv.SupportSession.Status == "cancelled")) {
				conv.Round++
			}
			conv.Status = "waiting"
			conv.AssignedAdminID = 0
		} else {
			if adminID == 0 || version != conv.Version {
				return ErrConflict
			}
			if action != "claim" && !(action == "ai" && (conv.Status == "waiting" || conv.Status == "closed")) && conv.AssignedAdminID != adminID {
				return ErrConflict
			}
			switch action {
			case "claim", "transfer":
				if action == "claim" {
					if conv.Status != "waiting" {
						return ErrConflict
					}
					assignee = adminID
				}
				var user models.AdminUser
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&user, assignee).Error; err != nil {
					return err
				}
				var active int64
				if err := tx.Model(&models.ChatConversation{}).Where("assigned_admin_id = ? AND status = ? AND id <> ?", assignee, "human", id).Count(&active).Error; err != nil {
					return err
				}
				capacity := cfg.Capacity
				if capacity <= 0 {
					capacity = 5
				}
				if active >= int64(capacity) {
					return ErrLimited
				}
				conv.Status = "human"
				conv.AssignedAdminID = assignee
				member := models.ChatMember{ConversationID: id, Realm: "admin", Subject: strconv.FormatUint(uint64(assignee), 10)}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error; err != nil {
					return err
				}
			case "cancel_close":
				var row models.SupportSession
				if e := tx.Where("conversation_id = ? AND round = ? AND status = ?", id, conv.Round, "closing_pending").First(&row).Error; e != nil {
					return ErrClosureExpired
				}
			case "close":
				if conv.Status != "human" {
					return ErrConflict
				}
			case "ai":
				if conv.Status == "human" {
					return ErrRatingUnavailable
				}
				conv.Status = "ai"
				conv.AssignedAdminID = 0
			default:
				return ErrInvalid
			}
			if err := m.Cancel(tx, &conv, false, "接待状态已变更"); err != nil {
				return err
			}
		}
		if action == "ai" {
			if e := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ? AND status = ?", id, conv.Round, "waiting").Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version + 1")}).Error; e != nil {
				return e
			}
		}
		if action != "ai" {
			row := models.SupportSession{ConversationID: id, Round: conv.Round, Status: "waiting"}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
				return e
			}
			state := "active"
			if action == "human" {
				state = "waiting"
			}
			if action == "close" {
				state = "closing_pending"
			}
			closingRequestedAt := int64(0)
			if action == "close" {
				closingRequestedAt = time.Now().UnixMilli()
			}
			updates := map[string]any{"closing_requested_at": closingRequestedAt, "status": state, "assigned_admin_id": conv.AssignedAdminID, "version": gorm.Expr("version + 1")}
			if action == "claim" || action == "transfer" {
				updates["started_at"] = gorm.Expr("CASE WHEN started_at = 0 THEN ? ELSE started_at END", time.Now().UnixMilli())
			}
			if e := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ?", id, conv.Round).Updates(updates).Error; e != nil {
				return e
			}
		}
		if err := tx.Model(&conv).Updates(map[string]any{"status": conv.Status, "assigned_admin_id": conv.AssignedAdminID, "version": conv.Version}).Error; err != nil {
			return err
		}
		if err := m.receptionEvent(tx, &conv); err != nil {
			return err
		}
		payload := map[string]any{"status": conv.Status, "assigned_admin_id": conv.AssignedAdminID, "version": conv.Version, "round": conv.Round}
		if conv.SupportSession != nil {
			payload["session_id"] = conv.SupportSession.ID
			payload["session_version"] = conv.SupportSession.Version
		}
		message := models.ChatMessage{SenderRealm: "system", SenderSubject: "support", Type: "event", Content: action, Payload: Raw(payload)}
		if err := m.Message(tx, &conv, &message); err != nil {
			return err
		}
		return nil
	})
	if err == nil && conv.Status == "human" {
		_ = m.Redis.SetNX(ctx, m.Key(constants.TempAgentPresence, conv.AssignedAdminID), "0", constants.TempAgentPresence.TTL).Err()
	}
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return conv, err
}

type Preview struct {
	Content   string `json:"content"`
	Sequence  uint64 `json:"sequence"`
	UpdatedAt int64  `json:"updated_at"`
	Device    string `json:"device"`
}

// PreviewInput 只接受当前客户的会话，快照与消息历史分离。
func (m *Manager) PreviewInput(ctx context.Context, p realtime.Principal, conversationID uint, content string, sequence uint64) error {
	if err := m.Validate(ctx, p); err != nil {
		return err
	}
	record, err := m.Sessions.Read(ctx, p.SessionID)
	if err != nil {
		return err
	}
	session := Session{Record: record}
	conv, err := m.Conversation(ctx, conversationID, session, 0, "")
	if err != nil {
		return err
	}
	_, cfg, _, err := m.App(ctx, conv.AppID)
	if err != nil {
		return err
	}
	if !cfg.InputPreview {
		return nil
	}
	if len(content) > 16000 {
		return ErrInvalid
	}
	preview := Preview{Content: content, Sequence: sequence, UpdatedAt: time.Now().UnixMilli(), Device: TokenHash(p.SessionID)[:16]}
	key := m.Key(constants.TempInputPreview, conv.ID, preview.Device)
	accepted, err := m.Redis.Eval(ctx, `local old=redis.call('GET',KEYS[1]);if old and cjson.decode(old).sequence>=tonumber(ARGV[1]) then return 0 end;redis.call('SET',KEYS[1],ARGV[2],'EX',ARGV[3]);return 1`, []string{key}, sequence, string(Raw(preview)), int(constants.TempInputPreview.TTL.Seconds())).Int()
	if err != nil || accepted == 0 {
		return err
	}
	acceptedSQL := false
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member models.ChatMember
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND realm = ? AND subject = ?", conv.ID, p.Realm, p.Subject).First(&member).Error; e != nil {
			return e
		}
		var previous Preview
		_ = json.Unmarshal(member.Extra, &previous)
		if previous.Device == preview.Device && previous.Sequence >= preview.Sequence {
			return nil
		}
		acceptedSQL = true
		return tx.Model(&member).Update("extra", Raw(preview)).Error
	})
	if err != nil || !acceptedSQL {
		return err
	}
	if conv.AssignedAdminID != 0 {
		_, err = m.Hub.Publish(ctx, realtime.Principal{Realm: "admin", Subject: strconv.FormatUint(uint64(conv.AssignedAdminID), 10)}, "support.input.updated", map[string]any{"conversation_id": conv.ID, "preview": preview})
	}
	return err
}
