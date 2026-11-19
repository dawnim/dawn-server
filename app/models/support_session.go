package models

// SupportSession保存一轮人工接待，结束确认和评价不随下一次咨询被覆盖。
type SupportSession struct {
	ID                 uint   `json:"id" gorm:"primaryKey"`
	ConversationID     uint   `json:"conversation_id" gorm:"not null;uniqueIndex:idx_support_session_round;index;comment:聊天会话ID"`
	Round              uint   `json:"round" gorm:"not null;uniqueIndex:idx_support_session_round;comment:接待轮次"`
	AssignedAdminID    uint   `json:"assigned_admin_id" gorm:"not null;default:0;index;comment:负责客服ID"`
	Status             string `json:"status" gorm:"not null;size:20;default:waiting;index:idx_support_session_status;comment:waiting排队 active接待 closing_pending等待客户确认 closed结束 cancelled取消排队"`
	Version            uint   `json:"version" gorm:"not null;default:1;comment:接待状态版本"`
	Rating             *int   `json:"rating" gorm:"comment:评分1差评 2一般 3好评:空表示未评价"`
	Comment            string `json:"comment" gorm:"not null;type:text;comment:客户评价"`
	StartedAt          int64  `json:"started_at" gorm:"not null;default:0;comment:首次接待时间(Unix毫秒)，0表示尚未接待"`
	OfflineSince       int64  `json:"offline_since" gorm:"not null;default:0;comment:持续离线起点(Unix毫秒)，0表示未离线"`
	LastOnlineAt       int64  `json:"last_online_at" gorm:"not null;default:0;comment:最近确认在线时间(Unix毫秒)"`
	ClosingRequestedAt int64  `json:"closing_requested_at" gorm:"not null;default:0;comment:申请结束时间(Unix毫秒)"`
	CloseReason        string `json:"close_reason" gorm:"not null;default:'';size:32;comment:customer客户结束 offline_timeout离线结束 queue_offline_timeout离线取消排队"`
	ClosedAt           int64  `json:"closed_at" gorm:"not null;default:0;comment:结束时间(Unix毫秒)"`
	CreatedAt          int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt          int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
}

func (SupportSession) TableName() string { return "support_sessions" }
