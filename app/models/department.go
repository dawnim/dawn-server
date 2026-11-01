package models

type Department struct {
	Base
	Name     string `json:"name" gorm:"not null;size:100;comment:部门名称" binding:"required,max=100"`
	ParentID uint   `json:"parent_id" gorm:"not null;default:0;index;comment:上级部门ID:0为顶级"`
	Leader   string `json:"leader" gorm:"not null;size:100;comment:负责人"`
	Phone    string `json:"phone" gorm:"not null;size:40;comment:联系电话"`
	Email    string `json:"email" gorm:"not null;size:180;comment:联系邮箱"`
	Sort     int    `json:"sort" gorm:"not null;default:0;comment:排序:升序"`
	Status   int    `json:"status" gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Remark   string `json:"remark" gorm:"not null;comment:备注;size:500"`
}

func (Department) TableName() string { return "admin_departments" }
