package models

import (
	"gorm.io/plugin/soft_delete"
)

// Base 复用主键、UTC 时间和软删除字段.
type Base struct {
	ID        uint                  `json:"id" gorm:"not null;primaryKey"`
	CreatedAt int64                 `json:"created_at" gorm:"not null;autoCreateTime:milli"`
	UpdatedAt int64                 `json:"updated_at" gorm:"not null;autoUpdateTime:milli"`
	DeletedAt soft_delete.DeletedAt `json:"-" gorm:"not null;softDelete:milli;index;default:0"`
}

const (
	Enabled  = 1
	Disabled = 2
)
