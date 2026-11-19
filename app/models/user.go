package models

import (
	"crypto/rand"
	"math"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/identity"
	"gorm.io/gorm"
)

const (
	UserGuest  = "guest"
	UserMember = "member"
	UserAI     = "ai"
)

// User 是可由业务模型嵌入扩展的通用用户；后台权限属于AdminUser。
type User struct {
	Base
	UniqueID         string  `json:"unique_id" gorm:"not null;size:32;uniqueIndex;comment:安装固定密钥生成的对外用户编号"`
	Type             string  `json:"type" gorm:"not null;size:16;default:guest;index;comment:用户类型:guest访客 member普通用户 ai智能体"`
	Username         *string `json:"username" binding:"omitempty,min=3,max=80" gorm:"size:80;uniqueIndex;comment:本站登录用户名"`
	PasswordHash     string  `json:"-" gorm:"not null;default:'';size:100;comment:密码哈希"`
	Nickname         string  `json:"nickname" binding:"max=100" gorm:"not null;default:'';size:100;comment:用户昵称"`
	Avatar           string  `json:"avatar" binding:"max=500" gorm:"not null;default:'';size:500;comment:头像地址"`
	Email            string  `json:"email" binding:"omitempty,email,max=180" gorm:"not null;default:'';size:180;comment:联系邮箱"`
	Mobile           string  `json:"mobile" binding:"max=40" gorm:"not null;default:'';size:40;comment:联系电话"`
	Locale           string  `json:"locale" binding:"max=16" gorm:"not null;size:16;default:zh;comment:偏好语言"`
	Status           int     `json:"status" binding:"oneof=1 2" gorm:"not null;default:1;comment:状态:1启用 2停用"`
	TelegramID       *int64  `json:"telegram_id" gorm:"uniqueIndex;comment:已绑定Telegram用户ID"`
	TelegramUsername string  `json:"telegram_username" gorm:"not null;default:'';size:80;comment:最近取得的Telegram用户名"`
}

func (User) TableName() string { return "users" }

// BeforeCreate为批量INSERT保留不同的占位值，事务提交前由AfterCreate转换。
func (u *User) BeforeCreate(*gorm.DB) error { u.UniqueID = rand.Text(); return nil }

func (u *User) AfterCreate(tx *gorm.DB) error {
	if uint64(u.ID) > math.MaxInt64 {
		return i18n.Errorf("用户编号超出范围")
	}
	c, err := identity.Load(tx.Statement.Context, tx)
	if err != nil {
		return err
	}
	u.UniqueID, err = c.ID2Big(int64(u.ID))
	if err != nil {
		return err
	}
	return tx.Session(&gorm.Session{NewDB: true}).Table(tx.Statement.Table).Where("id = ?", u.ID).UpdateColumn("unique_id", u.UniqueID).Error
}

// UserProfile是公开资料，不包含内部主键、权限和认证字段。
type UserProfile struct {
	UniqueID string  `json:"unique_id"`
	Type     string  `json:"type"`
	Username *string `json:"username"`
	Nickname string  `json:"nickname"`
	Avatar   string  `json:"avatar"`
	Email    string  `json:"email"`
	Mobile   string  `json:"mobile"`
	Locale   string  `json:"locale"`
}

func (u User) Profile() UserProfile {
	return UserProfile{UniqueID: u.UniqueID, Type: u.Type, Username: u.Username, Nickname: u.Nickname, Avatar: u.Avatar, Email: u.Email, Mobile: u.Mobile, Locale: u.Locale}
}
