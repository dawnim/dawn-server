package models

type Migration struct {
	Name      string `json:"name" gorm:"not null;size:150;primaryKey;comment:已执行的迁移版本"`
	CreatedAt int64  `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (Migration) TableName() string { return "admin_migrations" }
