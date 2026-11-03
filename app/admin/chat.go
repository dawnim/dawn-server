package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// DispatchChat publishes committed events at least once; publication is not a read receipt.
// WakeChat coalesces wakeups after commit; the SQL outbox remains authoritative.
func (a *App) WakeChat(ctx context.Context) {
	pipe := a.Redis.TxPipeline()
	key := a.Key(constants.TempChatDispatch)
	pipe.LPush(ctx, key, 1)
	pipe.LTrim(ctx, key, 0, 0)
	pipe.Expire(ctx, key, constants.TempChatDispatch.TTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("聊天通知等待定期补发", "error", err)
	}
}

func (a *App) DispatchChat(ctx context.Context) {
	nextCheck := time.Time{}
	for ctx.Err() == nil {
		if !time.Now().Before(nextCheck) {
			nextCheck = time.Now().Add(30 * time.Second)
			if err := a.PublishChatEvents(ctx); err != nil && ctx.Err() == nil {
				slog.Error("发布聊天事件失败", "error", err)
				nextCheck = time.Now().Add(5 * time.Second)
			}
		}
		// Short blocking Redis reads allow prompt shutdown without polling SQL.
		_, err := a.Redis.BLPop(ctx, time.Second, a.Key(constants.TempChatDispatch)).Result()
		if err == nil {
			nextCheck = time.Time{}
		}
		if err != nil && !errors.Is(err, redis.Nil) && ctx.Err() == nil {
			slog.Error("接收聊天通知失败", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// PublishChatEvents leases pending events. Consumers deduplicate event_id.
func (a *App) PublishChatEvents(ctx context.Context) error {
	db := a.DB.WithContext(ctx)
	now := time.Now().UnixMilli()
	events := []models.ChatEvent{}
	if err := db.Where("published_at = 0 AND available_at <= ?", now).Order("id").Limit(100).Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		lease := time.Now().Add(30 * time.Second).UnixMilli()
		result := db.Model(&models.ChatEvent{}).Where("id = ? AND published_at = 0 AND available_at <= ?", event.ID, now).Updates(map[string]any{"available_at": lease, "attempts": gorm.Expr("attempts + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		err := func() error {
			members := []models.ChatMember{}
			if err := db.Where("conversation_id = ?", event.ConversationID).Find(&members).Error; err != nil {
				return err
			}
			data := struct {
				EventID uint            `json:"event_id"`
				Data    json.RawMessage `json:"data"`
			}{event.ID, event.Data}
			for _, member := range members {
				if _, err := a.Realtime.Publish(ctx, realtime.Principal{Realm: member.Realm, Subject: member.Subject}, event.Type, data); err != nil {
					return err
				}
			}
			return nil
		}()
		values := map[string]any{"published_at": time.Now().UnixMilli()}
		if err != nil {
			slog.Error("聊天事件等待重试", "event_id", event.ID, "error", err)
			values = map[string]any{"available_at": time.Now().Add(time.Duration(min(event.Attempts+1, 60)) * time.Second).UnixMilli()}
		}
		if err := db.Model(&models.ChatEvent{}).Where("id = ? AND available_at = ? AND published_at = 0", event.ID, lease).Updates(values).Error; err != nil {
			return err
		}
	}
	if len(events) == 100 {
		a.WakeChat(ctx)
	}
	return nil
}
