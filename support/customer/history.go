package customer

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (m *Manager) History(ctx context.Context, id uint, session Session, adminID, before uint, size int) ([]models.ChatMessage, bool, error) {
	if _, err := m.Conversation(ctx, id, session, adminID, "view"); err != nil {
		return nil, false, err
	}
	q := m.DB.WithContext(ctx).Where("conversation_id = ?", id)
	if adminID == 0 {
		q = q.Where("visibility = ?", "public")
	}
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if size <= 0 {
		size = 10
	}
	rows := []models.ChatMessage{}
	if err := q.Order("id DESC").Limit(size + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > size
	if more {
		rows = rows[:size]
	}
	for i := range rows {
		if rows[i].Status == "streaming" {
			value, e := m.Redis.Get(ctx, m.Key(constants.TempAIStream, rows[i].ID)).Bytes()
			if e == nil {
				var snapshot struct {
					Content string `json:"content"`
				}
				if json.Unmarshal(value, &snapshot) == nil {
					rows[i].Content = snapshot.Content
				}
			}
		}
	}
	return rows, more, nil
}

func (m *Manager) Read(ctx context.Context, id uint, session Session, adminID, messageID uint) error {
	conv, err := m.Conversation(ctx, id, session, adminID, "view")
	if err != nil {
		return err
	}
	realm, subject := Realm(conv.AppID), session.User.UniqueID
	if adminID != 0 {
		realm = "admin"
		subject = strconv.FormatUint(uint64(adminID), 10)
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message models.ChatMessage
		q := tx.Where("id = ? AND conversation_id = ?", messageID, id)
		if adminID == 0 {
			q = q.Where("visibility = ?", "public")
		}
		if e := q.First(&message).Error; e != nil {
			return e
		}
		if adminID != 0 {
			observer := models.ChatMember{ConversationID: id, Realm: realm, Subject: subject}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&observer).Error; e != nil {
				return e
			}
		}
		var member models.ChatMember
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("conversation_id = ? AND realm = ? AND subject = ?", id, realm, subject).First(&member).Error; e != nil {
			return e
		}
		r := tx.Model(&models.ChatMember{}).Where("conversation_id = ? AND realm = ? AND subject = ? AND last_read_message_id < ?", id, realm, subject, messageID).Update("last_read_message_id", messageID)
		if r.Error != nil || r.RowsAffected == 0 {
			return r.Error
		}
		return m.Event(tx, id, "chat.read.updated", map[string]any{"conversation_id": id, "realm": realm, "subject": subject, "message_id": messageID})
	})
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return err
}

func (m *Manager) SetPresence(ctx context.Context, adminID uint, online bool) error {
	if err := m.AuthorizeAdmin(ctx, adminID, "reply"); err != nil {
		return err
	}
	key := m.Key(constants.TempAgentPresence, adminID)
	value := "0"
	if online {
		value = "1"
	}
	return m.Redis.Set(ctx, key, value, constants.TempAgentPresence.TTL).Err()
}

// AgentHeartbeat只续租并读取共享接待状态，其他窗口不会覆盖人工设置的状态。
func (m *Manager) AgentHeartbeat(ctx context.Context, adminID uint) (bool, error) {
	if err := m.AuthorizeAdmin(ctx, adminID, "reply"); err != nil {
		return false, err
	}
	value, err := m.Redis.Eval(ctx, `local v=redis.call('GET',KEYS[1]);if not v then v='0' end;redis.call('SET',KEYS[1],v,'PX',ARGV[1]);return v`, []string{m.Key(constants.TempAgentPresence, adminID)}, constants.TempAgentPresence.TTL.Milliseconds()).Text()
	return value == "1", err
}

func (m *Manager) AssignWaiting(ctx context.Context) error {

	var abandoned []models.ChatConversation
	if err := m.DB.WithContext(ctx).Where("type = ? AND status = ?", "support", "human").Find(&abandoned).Error; err != nil {
		return err
	}
	for _, conv := range abandoned {
		alive, err := m.Redis.Exists(ctx, m.Key(constants.TempAgentPresence, conv.AssignedAdminID)).Result()
		if err != nil {
			return err
		}
		if alive > 0 && m.AgentAccess(ctx, conv.AppID, conv.AssignedAdminID, "reply") == nil {
			continue
		}
		err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&models.ChatConversation{}).Where("id = ? AND version = ? AND status = ?", conv.ID, conv.Version, "human").Updates(map[string]any{"status": "waiting", "assigned_admin_id": 0, "version": gorm.Expr("version + 1")})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			if e := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ? AND status <> ?", conv.ID, conv.Round, "closed").Updates(map[string]any{"status": "waiting", "assigned_admin_id": 0, "version": gorm.Expr("version + 1")}).Error; e != nil {
				return e
			}
			conv.Status = "waiting"
			conv.AssignedAdminID = 0
			conv.Version++
			message := models.ChatMessage{SenderRealm: "system", SenderSubject: "support", Type: "event", Content: "客服已离线，正在重新分配", Payload: Raw(map[string]any{"status": "waiting", "assigned_admin_id": 0, "version": conv.Version, "round": conv.Round})}
			if e := m.Message(tx, &conv, &message); e != nil {
				return e
			}
			return m.receptionEvent(tx, &conv)
		})
		if err != nil {
			return err
		}
		if m.Wake != nil {
			m.Wake(ctx)
		}
	}
	var rows []models.ChatConversation
	return m.DB.WithContext(ctx).Where("type = ? AND status = ? AND last_message_id > 0", "support", "waiting").FindInBatches(&rows, 100, func(_ *gorm.DB, _ int) error {
		for _, conv := range rows {
			session, err := m.CurrentSession(ctx, conv.ID)
			if err != nil {
				return err
			}
			if session != nil && session.OfflineSince > 0 {
				continue
			}
			_, cfg, _, err := m.App(ctx, conv.AppID)
			if err != nil {
				continue
			}
			for _, id := range cfg.AgentIDs {
				online, e := m.Redis.Get(ctx, m.Key(constants.TempAgentPresence, id)).Result()
				if e != nil && e != redis.Nil {
					return e
				}
				if online != "1" {
					continue
				}
				if _, e = m.Reception(ctx, conv.ID, Session{}, id, id, conv.Version, "claim"); e == nil {
					break
				}
			}
		}
		return nil
	}).Error
}
