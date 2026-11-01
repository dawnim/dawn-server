package models

// UserRole 用户与角色关联.
type UserRole struct {
	UserID uint `gorm:"not null;primaryKey;comment:管理员ID"`
	RoleID uint `gorm:"not null;primaryKey;index;comment:角色ID"`
}

func (UserRole) TableName() string { return "admin_user_roles" }
