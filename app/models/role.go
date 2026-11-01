package models

type Role struct {
	Base
	Name          string `json:"name" gorm:"not null;size:100;comment:角色名称" binding:"required,max=100"`
	Code          string `json:"code" gorm:"not null;size:100;uniqueIndex;comment:角色唯一编码" binding:"required,max=100"`
	Level         int    `json:"level" gorm:"not null;default:100;comment:权限级别:数值越小权限越高"`
	Status        int    `json:"status" gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Sort          int    `json:"sort" gorm:"not null;default:0;comment:排序:升序"`
	Remark        string `json:"remark" gorm:"not null;comment:备注;size:500"`
	DataScope     string `json:"data_scope" gorm:"not null;size:30;default:self;comment:数据范围:all全部 self本人 department本部门 departments指定部门"`
	DepartmentIDs []uint `json:"department_ids" gorm:"not null;serializer:json;default:'[]';comment:指定部门ID列表(JSON)"`
}

func (Role) TableName() string { return "admin_roles" }

const (
	ScopeAll         = "all"
	ScopeSelf        = "self"
	ScopeDepartment  = "department"
	ScopeDepartments = "departments"
)
