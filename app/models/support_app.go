package models

import "encoding/json"

type SupportApp struct {
	Base
	Name          string          `json:"name" gorm:"not null;size:100;comment:客服应用名称"`
	CreatedBy     uint            `json:"created_by" gorm:"not null;default:0;index;comment:创建管理员ID"`
	PublicKey     string          `json:"public_key" gorm:"not null;size:64;uniqueIndex;comment:公开接入标识"`
	Avatar        string          `json:"avatar" gorm:"not null;default:'';size:500;comment:品牌头像"`
	AIUserID      uint            `json:"ai_user_id" gorm:"not null;comment:应用AI用户ID"`
	Status        int             `json:"status" gorm:"not null;default:1;comment:状态:1启用 2停用"`
	TelegramBotID *int64          `json:"telegram_bot_id" gorm:"uniqueIndex;comment:应用独占的Telegram机器人ID"`
	Config        json.RawMessage `json:"config" gorm:"type:json;comment:入口、模型、知识、工具与接待配置"`
	Secrets       string          `json:"-" gorm:"not null;default:'';type:text;comment:加密保存的模型、身份与机器人密钥"`
	Revision      uint            `json:"revision" gorm:"not null;default:1;comment:配置并发控制版本"`
}

func (SupportApp) TableName() string { return "support_apps" }
