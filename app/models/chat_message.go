package models

import "encoding/json"

type ChatMessage struct {
	ID             uint            `json:"id" gorm:"primaryKey;index:idx_chat_message_history,priority:2"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;index:idx_chat_message_history,priority:1;comment:会话ID"`
	SenderRealm    string          `json:"sender_realm" gorm:"not null;size:32;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:1;comment:实际发送者身份域"`
	SenderSubject  string          `json:"sender_subject" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;index:idx_chat_message_sender_time,priority:2;comment:实际发送者主体ID"`
	RequestID      string          `json:"request_id" gorm:"not null;size:64;uniqueIndex:idx_chat_message_request;comment:发送幂等标识"`
	Type           string          `json:"type" gorm:"not null;size:24;default:text;comment:消息类型:text文本 event事件"`
	Status         string          `json:"status" gorm:"not null;size:16;default:completed;comment:queued streaming completed failed cancelled"`
	Content        string          `json:"content" gorm:"not null;type:text;comment:消息正文"`
	ReplyToID      uint            `json:"reply_to_id" gorm:"not null;default:0;comment:同会话被回复消息ID"`
	Payload        json.RawMessage `json:"payload" gorm:"type:json;comment:按消息类型定义的扩展数据"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli;index:idx_chat_message_sender_time,priority:3"`
	UpdatedAt      int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatMessage) TableName() string { return "chat_messages" }
