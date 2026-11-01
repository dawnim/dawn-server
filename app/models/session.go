package models

// Session 仅保存凭证摘要, 每次请求重新读取账号和角色状态.
type Session struct {
	ID               string `json:"id" gorm:"not null;size:64;primaryKey"`
	UserID           uint   `json:"user_id" gorm:"not null;default:0;index;comment:管理员ID"`
	AccessHash       string `json:"-" gorm:"not null;size:64;uniqueIndex;comment:访问凭证摘要"`
	RefreshHash      string `json:"-" gorm:"not null;size:64;uniqueIndex;comment:刷新凭证摘要"`
	ExpiresAt        int64  `json:"expires_at" gorm:"not null;default:0;index;comment:访问凭证到期时间(Unix毫秒)"`
	RefreshExpiresAt int64  `json:"-" gorm:"not null;default:0;index;comment:刷新凭证到期时间(Unix毫秒)"`
	CreatedAt        int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	IP               string `json:"ip" gorm:"not null;size:80;comment:登录IP"`
	UserAgent        string `json:"user_agent" gorm:"not null;size:500;comment:客户端信息"`
}

func (Session) TableName() string { return "admin_sessions" }
