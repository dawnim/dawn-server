package models

type Setting struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	Key       string `json:"key" gorm:"not null;size:100;uniqueIndex;comment:配置唯一标识"`
	Name      string `json:"name" gorm:"not null;size:100;comment:配置名称"`
	Value     string `json:"value" gorm:"not null;type:json;comment:配置内容JSON:保密字段保存密文"`
	Revision  int64  `json:"revision" gorm:"not null;default:1;comment:配置版本:防止并发写入旧缓存"`
	CreatedAt int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
	UpdatedBy uint   `json:"updated_by" gorm:"not null;default:0;comment:最后修改管理员ID"`
}

func (Setting) TableName() string { return "admin_settings" }
