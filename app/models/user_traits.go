package models

import "encoding/json"

type UserTraits struct {
	AppID            uint            `json:"app_id" gorm:"primaryKey;uniqueIndex:idx_traits_identity,priority:1;comment:客服应用ID"`
	UserID           uint            `json:"user_id" gorm:"primaryKey;comment:普通用户ID"`
	IdentityProvider *string         `json:"identity_provider" gorm:"size:100;uniqueIndex:idx_traits_identity,priority:2;comment:可信外部身份来源"`
	ExternalUserID   *string         `json:"external_user_id" gorm:"size:180;uniqueIndex:idx_traits_identity,priority:3;comment:已验证外部用户编号"`
	VerifiedAt       int64           `json:"verified_at" gorm:"not null;default:0;comment:最近身份验证时间(Unix毫秒)"`
	Attributes       json.RawMessage `json:"attributes" gorm:"type:json;comment:接入方提交的扩展资料"`
	Observed         json.RawMessage `json:"observed" gorm:"type:json;comment:访问环境与近期浏览轨迹"`
	Remark           string          `json:"remark" gorm:"not null;default:'';type:text;comment:应用内客服备注"`
	FirstSeenAt      int64           `json:"first_seen_at" gorm:"not null;autoCreateTime:milli;comment:首次访问时间(Unix毫秒)"`
	LastSeenAt       int64           `json:"last_seen_at" gorm:"not null;autoUpdateTime:milli;comment:最近访问时间(Unix毫秒)"`
}

func (UserTraits) TableName() string { return "user_traits" }
