package models

import "encoding/json"

type ChatEvent struct {
	ID             uint            `json:"id" gorm:"primaryKey;index:idx_chat_event_pending,priority:3"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;index;comment:会话ID"`
	Type           string          `json:"type" gorm:"not null;size:64;comment:实时事件类型"`
	Data           json.RawMessage `json:"data" gorm:"not null;type:json;comment:公开事件数据"`
	Attempts       int             `json:"-" gorm:"not null;default:0;comment:投递尝试次数"`
	AvailableAt    int64           `json:"-" gorm:"not null;default:0;index:idx_chat_event_pending,priority:2;comment:下次投递或租约到期时间"`
	PublishedAt    int64           `json:"-" gorm:"not null;default:0;index:idx_chat_event_pending,priority:1;comment:发布到WS收件流的时间"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (ChatEvent) TableName() string { return "chat_events" }
