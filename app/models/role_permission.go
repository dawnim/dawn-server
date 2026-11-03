package models

type RolePermission struct {
	RoleID uint   `json:"role_id" gorm:"not null;primaryKey;comment:角色ID"`
	Key    string `json:"key" gorm:"not null;size:150;primaryKey;comment:授权权限Key"`
}

func (RolePermission) TableName() string { return "admin_role_permissions" }
