package models

import "encoding/json"

type AIRun struct {
	RequestKey     *string         `json:"-" gorm:"size:180;uniqueIndex;comment:跨渠道执行幂等标识"`
	ID             uint            `json:"id" gorm:"primaryKey"`
	AppID          uint            `json:"app_id" gorm:"not null;index;comment:所属客服应用ID"`
	ConversationID uint            `json:"conversation_id" gorm:"not null;default:0;index;comment:所属会话ID"`
	MessageID      uint            `json:"message_id" gorm:"not null;default:0;comment:触发消息ID"`
	ReplyID        uint            `json:"reply_id" gorm:"not null;default:0;comment:回复消息ID"`
	ParentID       uint            `json:"parent_id" gorm:"not null;default:0;index;comment:父执行ID:0为根执行"`
	Kind           string          `json:"kind" gorm:"not null;size:32;comment:reply回复 draft人工草稿 import知识导入 index索引重建 tool工具"`
	Status         string          `json:"status" gorm:"not null;size:20;default:queued;index:idx_ai_pending,priority:1;comment:queued running completed failed cancelled waiting"`
	Version        uint            `json:"version" gorm:"not null;default:1;comment:绑定的会话或知识版本"`
	Attempt        uint            `json:"attempt" gorm:"not null;default:0;comment:领取次数与流式尝试编号"`
	LeaseToken     string          `json:"-" gorm:"not null;default:'';size:64;comment:领取者随机租约"`
	AvailableAt    int64           `json:"available_at" gorm:"not null;default:0;index:idx_ai_pending,priority:2;comment:可领取或租约到期时间(Unix毫秒)"`
	DeadlineAt     int64           `json:"deadline_at" gorm:"not null;default:0;comment:整体截止时间(Unix毫秒)"`
	Input          json.RawMessage `json:"input" gorm:"type:json;comment:经白名单处理的执行输入"`
	Output         json.RawMessage `json:"output" gorm:"type:json;comment:执行结果、来源与恢复检查点"`
	Error          string          `json:"error" gorm:"not null;default:'';type:text;comment:脱敏执行错误"`
	CreatedAt      int64           `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt      int64           `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (AIRun) TableName() string { return "ai_runs" }
