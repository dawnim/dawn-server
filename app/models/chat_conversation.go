package models

type ChatConversation struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	Type          string `json:"type" gorm:"not null;size:16;comment:会话类型:direct单聊 support客服"`
	Identify      string `json:"-" gorm:"not null;size:180;uniqueIndex;comment:包含身份域的会话唯一标识"`
	LastMessageID uint   `json:"last_message_id" gorm:"not null;default:0;index;comment:最后消息ID"`
	LastMessageAt int64  `json:"last_message_at" gorm:"not null;default:0;comment:最后消息时间(Unix毫秒)"`
	CreatedAt     int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt     int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatConversation) TableName() string { return "chat_conversations" }
