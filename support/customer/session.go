package customer

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CurrentSession返回最近一轮人工接待，AI咨询可以没有人工接待记录。
func (m *Manager) CurrentSession(ctx context.Context, conversationID uint) (*models.SupportSession, error) {
	var row models.SupportSession
	r := m.DB.WithContext(ctx).Where("conversation_id = ?", conversationID).Order("id DESC").Limit(1).Find(&row)
	if r.Error != nil || r.RowsAffected == 0 {
		return nil, r.Error
	}
	return &row, nil
}

func (m *Manager) receptionEvent(tx *gorm.DB, conv *models.ChatConversation) error {
	var row models.SupportSession
	r := tx.Where("conversation_id = ?", conv.ID).Order("id DESC").Limit(1).Find(&row)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected > 0 {
		conv.SupportSession = &row
	} else {
		conv.SupportSession = nil
	}
	return m.Event(tx, conv.ID, "support.reception.updated", map[string]any{"conversation_id": conv.ID, "conversation": conv})
}

// endReception在调用方事务内结束本轮人工接待。
func (m *Manager) endReception(tx *gorm.DB, conv *models.ChatConversation, row *models.SupportSession, reason string) error {
	row.Status = "closed"
	row.CloseReason = reason
	if reason == "queue_offline_timeout" {
		row.Status = "cancelled"
	}
	row.ClosedAt = time.Now().UnixMilli()
	row.Version++
	if e := tx.Save(row).Error; e != nil {
		return e
	}
	conv.Status = "closed"
	conv.AssignedAdminID = 0
	if e := m.Cancel(tx, conv, false, "接待已结束"); e != nil {
		return e
	}
	if e := tx.Model(conv).Updates(map[string]any{"status": "closed", "assigned_admin_id": 0}).Error; e != nil {
		return e
	}
	event := "closing_accepted"
	if reason == "offline_timeout" {
		event = "offline_closed"
	}
	if reason == "queue_offline_timeout" {
		event = "offline_cancelled"
	}
	message := models.ChatMessage{SenderRealm: "system", SenderSubject: "support", Type: "event", Content: event, Payload: Raw(map[string]any{"session_id": row.ID, "session_version": row.Version})}
	return m.Message(tx, conv, &message)
}

// DecideClosure允许客户直接结束本轮接待，或拒绝客服的结束申请。
func (m *Manager) DecideClosure(ctx context.Context, id uint, visitor Session, sessionID, version uint, approved bool) (models.ChatConversation, error) {
	conv, err := m.Conversation(ctx, id, visitor, 0, "")
	if err != nil {
		return conv, err
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; e != nil {
			return e
		}
		var row models.SupportSession
		if e := tx.Where("id = ? AND conversation_id = ?", sessionID, id).First(&row).Error; e != nil {
			return e
		}
		if row.Status == "closed" && approved {
			return m.receptionEvent(tx, &conv)
		}
		if row.Status == "active" && !approved && row.Version == version+1 && row.Round == conv.Round {
			return m.receptionEvent(tx, &conv)
		}
		if row.Version != version || row.Round != conv.Round || (row.Status != "closing_pending" && (!approved || (row.Status != "active" && row.Status != "waiting"))) {
			return ErrClosureExpired
		}
		if approved {
			if e := m.endReception(tx, &conv, &row, "customer"); e != nil {
				return e
			}
		} else {
			row.Status = "active"
			row.ClosingRequestedAt = 0
			row.OfflineSince = 0
			row.LastOnlineAt = time.Now().UnixMilli()
			row.Version++
			if e := tx.Save(&row).Error; e != nil {
				return e
			}
			if e := m.Cancel(tx, &conv, false, "客户继续咨询"); e != nil {
				return e
			}
			message := models.ChatMessage{SenderRealm: "system", SenderSubject: "support", Type: "event", Content: "closing_rejected", Payload: Raw(map[string]any{"session_id": row.ID, "session_version": row.Version})}
			if e := m.Message(tx, &conv, &message); e != nil {
				return e
			}
		}
		return m.receptionEvent(tx, &conv)
	})
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return conv, err
}

// Rate在待确认卡片中一次完成评分与结束，也允许评价已结束的历史接待。
func (m *Manager) Rate(ctx context.Context, id uint, visitor Session, sessionID, version uint, rating int, comment string) error {
	conv, err := m.Conversation(ctx, id, visitor, 0, "")
	if err != nil {
		return err
	}
	if rating < 1 || rating > 3 || utf8.RuneCountInString(comment) > 2000 {
		return ErrInvalid
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; e != nil {
			return e
		}
		var row models.SupportSession
		if e := tx.Where("id = ? AND conversation_id = ?", sessionID, id).First(&row).Error; e != nil {
			return e
		}
		if row.Status == "closing_pending" && row.Version != version {
			return ErrClosureExpired
		}
		if row.Status != "closed" && (row.Status != "closing_pending" || row.Round != conv.Round) {
			return ErrRatingUnavailable
		}
		if row.Rating != nil {
			if *row.Rating == rating && row.Comment == comment {
				return nil
			}
			return ErrRated
		}
		if row.Status == "closing_pending" {
			if e := m.endReception(tx, &conv, &row, "customer"); e != nil {
				return e
			}
		}
		if e := tx.Model(&row).Updates(map[string]any{"rating": rating, "comment": comment, "version": gorm.Expr("version + 1")}).Error; e != nil {
			return e
		}
		message := models.ChatMessage{SenderRealm: Realm(conv.AppID), SenderSubject: visitor.User.UniqueID, Type: "event", Content: "客户评价", Payload: Raw(map[string]any{"session_id": row.ID, "round": row.Round, "rating": rating, "comment": comment})}
		if e := m.Message(tx, &conv, &message); e != nil {
			return e
		}
		return m.receptionEvent(tx, &conv)
	})
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return err
}

// RetryReply重试当前AI接待中的失败回复，不重复创建客户消息。
func (m *Manager) RetryReply(ctx context.Context, id uint, visitor Session, messageID uint) error {
	conv, err := m.Conversation(ctx, id, visitor, 0, "")
	if err != nil {
		return err
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, id).Error; e != nil {
			return e
		}
		if conv.Status != "ai" {
			return ErrConflict
		}
		var run models.AIRun
		if e := tx.Where("conversation_id = ? AND reply_id = ? AND kind = ?", id, messageID, "reply").First(&run).Error; e != nil {
			return e
		}
		if run.Version != conv.Version {
			return ErrConflict
		}
		if run.Status == "queued" || run.Status == "running" {
			return nil
		}
		if run.Status != "failed" && run.Status != "cancelled" {
			return ErrConflict
		}
		if e := m.Redis.Del(ctx, m.Key(constants.TempAIStream, messageID)).Err(); e != nil {
			return e
		}
		if e := tx.Model(&run).Updates(map[string]any{"status": "queued", "error": "", "output": nil, "available_at": 0, "deadline_at": time.Now().Add(3 * time.Minute).UnixMilli()}).Error; e != nil {
			return e
		}
		var reply models.ChatMessage
		if e := tx.First(&reply, messageID).Error; e != nil {
			return e
		}
		reply.Status = "queued"
		reply.Content = ""
		reply.Payload = nil
		if e := tx.Model(&reply).Select("status", "content", "payload").Updates(&reply).Error; e != nil {
			return e
		}
		return m.Event(tx, id, "chat.message.updated", reply)
	})
	if err == nil {
		if m.Wake != nil {
			m.Wake(ctx)
		}
	}
	return err
}
