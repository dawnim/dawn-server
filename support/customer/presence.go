package customer

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OfflineDeadline 计算当前接待的离线截止时间，0表示不启用自动结束。
func (cfg Config) OfflineDeadline(row *models.SupportSession) int64 {
	if row == nil || row.OfflineSince == 0 || row.Status == "closed" || row.Status == "cancelled" {
		return 0
	}
	minutes := 10
	if cfg.OfflineTimeoutMinutes != nil {
		minutes = *cfg.OfflineTimeoutMinutes
	}
	if minutes == 0 {
		return 0
	}
	deadline := row.OfflineSince + int64(minutes)*time.Minute.Milliseconds()
	closing := 3
	if cfg.ClosingOfflineMinutes != nil {
		closing = *cfg.ClosingOfflineMinutes
	}
	if row.Status == "closing_pending" && closing > 0 {
		deadline = min(deadline, max(row.OfflineSince, row.ClosingRequestedAt)+int64(closing)*time.Minute.Milliseconds())
	}
	return deadline
}

// VisitorConnected 仅重置当前网站接待的离线计时，不恢复已经结束的接待。
func (m *Manager) VisitorConnected(ctx context.Context, p realtime.Principal) error {
	if !strings.HasPrefix(p.Realm, "support:") {
		return nil
	}
	appID, err := strconv.ParseUint(strings.TrimPrefix(p.Realm, "support:"), 10, 64)
	if err != nil {
		return ErrInvalid
	}
	var conversations []models.ChatConversation
	if err = m.DB.WithContext(ctx).Where("app_id = ? AND user_id IN (?) AND status IN ?", appID, m.DB.Model(&models.User{}).Select("id").Where("unique_id = ?", p.Subject), []string{"human", "waiting"}).Find(&conversations).Error; err != nil {
		return err
	}
	for _, conv := range conversations {
		if err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, conv.ID).Error; e != nil {
				return e
			}
			result := tx.Model(&models.SupportSession{}).Where("conversation_id = ? AND round = ? AND status IN ?", conv.ID, conv.Round, []string{"waiting", "active", "closing_pending"}).Updates(map[string]any{"offline_since": 0, "last_online_at": time.Now().UnixMilli()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				return m.receptionEvent(tx, &conv)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if len(conversations) > 0 && m.Wake != nil {
		m.Wake(ctx)
	}
	return nil
}

// ExpireOffline 由现有调度循环调用；Redis异常或长时间中断后重新计算离线时间。
func (m *Manager) ExpireOffline(ctx context.Context) (err error) {
	changed := false
	defer func() {
		if changed && err == nil && m.Wake != nil {
			m.Wake(ctx)
		}
	}()
	defer func() {
		if err != nil {
			m.presenceFailed.Store(true)
		}
	}()
	now := time.Now().UnixMilli()
	previous, err := m.Redis.Eval(ctx, `local v=redis.call('GET',KEYS[1]);redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2]);return tonumber(v) or 0`, []string{m.Key(constants.TempSupportPresenceCheck)}, now, constants.TempSupportPresenceCheck.TTL.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if m.presenceFailed.Swap(false) || previous == 0 || now-previous > constants.TempSupportPresenceCheck.TTL.Milliseconds() {
		return m.DB.WithContext(ctx).Model(&models.SupportSession{}).Where("status IN ?", []string{"waiting", "active", "closing_pending"}).Update("offline_since", 0).Error
	}
	var sessions []models.SupportSession
	return m.DB.WithContext(ctx).Where("status IN ?", []string{"waiting", "active", "closing_pending"}).FindInBatches(&sessions, 100, func(_ *gorm.DB, _ int) error {
		for _, candidate := range sessions {
			if e := m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var conv models.ChatConversation
				if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, candidate.ConversationID).Error; e != nil {
					return e
				}
				if conv.Round != candidate.Round || (conv.Status != "human" && conv.Status != "waiting") {
					return nil
				}
				var channel struct {
					Channel string `json:"channel"`
				}
				_ = json.Unmarshal(conv.Context, &channel)
				if channel.Channel == "telegram" {
					return nil
				}
				var row models.SupportSession
				if e := tx.First(&row, candidate.ID).Error; e != nil {
					return e
				}
				if row.Version != candidate.Version || row.Round != conv.Round || (row.Status != "active" && row.Status != "waiting" && row.Status != "closing_pending") {
					return nil
				}
				var user models.User
				if e := tx.Select("unique_id").First(&user, conv.UserID).Error; e != nil {
					return e
				}
				seen, e := m.Hub.Presence(ctx, realtime.Principal{Realm: Realm(conv.AppID), Subject: user.UniqueID})
				if e != nil {
					return e
				}
				if seen > 0 {
					wasOffline := row.OfflineSince != 0
					row.OfflineSince = 0
					row.LastOnlineAt = seen
					if e = tx.Model(&row).Select("offline_since", "last_online_at").Updates(&row).Error; e != nil {
						return e
					}
					if wasOffline {
						changed = true
						return m.receptionEvent(tx, &conv)
					}
					return nil
				}
				if row.OfflineSince == 0 {
					changed = true
					row.OfflineSince = now
					if e = tx.Model(&row).Update("offline_since", now).Error; e != nil {
						return e
					}
					return m.receptionEvent(tx, &conv)
				}
				var app models.SupportApp
				if e = tx.Select("config").First(&app, conv.AppID).Error; e != nil {
					return e
				}
				var cfg Config
				if e = json.Unmarshal(app.Config, &cfg); e != nil {
					return e
				}
				deadline := cfg.OfflineDeadline(&row)
				if deadline == 0 || deadline > now {
					return nil
				}
				// 再检查一次连接，重连与接待修改均在同一会话锁下确认。
				if seen, e = m.Hub.Presence(ctx, realtime.Principal{Realm: Realm(conv.AppID), Subject: user.UniqueID}); e != nil {
					return e
				} else if seen > 0 {
					return nil
				}
				reason := "offline_timeout"
				if row.Status == "waiting" && row.StartedAt == 0 {
					reason = "queue_offline_timeout"
				}
				changed = true
				if e = m.endReception(tx, &conv, &row, reason); e != nil {
					return e
				}
				return m.receptionEvent(tx, &conv)
			}); e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					continue
				}
				return e
			}
		}
		return nil
	}).Error
}

// FillVisitorPresence 补充后台展示信息，Telegram及Redis不可用时不报告离线。
func (m *Manager) FillVisitorPresence(ctx context.Context, conv *models.ChatConversation, subject string) error {
	var channel struct {
		Channel string `json:"channel"`
	}
	_ = json.Unmarshal(conv.Context, &channel)
	if channel.Channel == "telegram" {
		return nil
	}
	seen, err := m.Hub.Presence(ctx, realtime.Principal{Realm: Realm(conv.AppID), Subject: subject})
	if err != nil {
		return nil
	}
	online := seen > 0
	conv.VisitorOnline = &online
	if conv.SupportSession == nil {
		conv.SupportSession, err = m.CurrentSession(ctx, conv.ID)
		if err != nil {
			return err
		}
	}
	conv.VisitorLastOnlineAt = seen
	if conv.SupportSession != nil {
		conv.VisitorLastOnlineAt = max(seen, conv.SupportSession.LastOnlineAt)
		if !online {
			var app models.SupportApp
			if err = m.DB.WithContext(ctx).Select("config").First(&app, conv.AppID).Error; err != nil {
				return err
			}
			var cfg Config
			if err = json.Unmarshal(app.Config, &cfg); err != nil {
				return err
			}
			conv.VisitorOfflineEndAt = cfg.OfflineDeadline(conv.SupportSession)
		}
	}
	return nil
}
