package models

type ChatMember struct {
	ConversationID    uint   `json:"conversation_id" gorm:"primaryKey;comment:会话ID"`
	Realm             string `json:"realm" gorm:"primaryKey;size:32;index:idx_chat_member_identity;comment:身份域:admin或业务定义"`
	Subject           string `json:"subject" gorm:"primaryKey;size:64;index:idx_chat_member_identity;comment:身份域内的主体ID"`
	LastReadMessageID uint   `json:"last_read_message_id" gorm:"not null;default:0;comment:已读消息位置"`
	CreatedAt         int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (ChatMember) TableName() string { return "chat_members" }
