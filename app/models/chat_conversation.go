package models

import "encoding/json"

type ChatConversation struct {
	VisitorOnline       *bool           `json:"visitor_online" gorm:"-"`
	VisitorLastOnlineAt int64           `json:"visitor_last_online_at" gorm:"-"`
	VisitorOfflineEndAt int64           `json:"visitor_offline_end_at" gorm:"-"`
	SupportSession      *SupportSession `json:"support_session,omitempty" gorm:"-"`
	ID                  uint            `json:"id" gorm:"primaryKey"`
	Type                string          `json:"type" gorm:"not null;size:16;comment:会话类型:direct单聊 support客服"`
	AppID               uint            `json:"app_id" gorm:"not null;default:0;index:idx_support_inbox,priority:1;comment:客服应用ID:0为管理员私聊"`
	UserID              uint            `json:"-" gorm:"not null;default:0;index;comment:客户用户ID"`
	EntryKey            string          `json:"entry_key" gorm:"not null;default:'';size:80;comment:入口稳定标识"`
	Status              string          `json:"status" gorm:"not null;size:16;default:ai;index:idx_support_inbox,priority:2;comment:ai智能接待 waiting等待人工 human人工接待 closed已结束"`
	AssignedAdminID     uint            `json:"assigned_admin_id" gorm:"not null;default:0;index;comment:当前负责管理员ID"`
	Version             uint            `json:"version" gorm:"not null;default:1;comment:接待并发控制版本"`
	Round               uint            `json:"round" gorm:"not null;default:1;comment:接待轮次"`
	Context             json.RawMessage `json:"context" gorm:"type:json;comment:渠道、外部消息、摘要与评价上下文"`
	Identify            string          `json:"-" gorm:"not null;size:180;uniqueIndex;comment:包含身份域的会话唯一标识"`
	LastMessageID       uint            `json:"last_message_id" gorm:"not null;default:0;index;comment:最后消息ID"`
	LastMessageAt       int64           `json:"last_message_at" gorm:"not null;default:0;comment:最后消息时间(Unix毫秒)"`
	CreatedAt           int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt           int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (ChatConversation) TableName() string { return "chat_conversations" }
