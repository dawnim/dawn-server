package models

type Category struct {
	Base
	Code     *string `json:"code" gorm:"size:30;uniqueIndex;comment:自动分类标识，空值为自建分类"`
	Name     string  `json:"name" gorm:"not null;size:100;comment:附件分类名称" binding:"required,max=100"`
	ParentID uint    `json:"parent_id" gorm:"not null;default:0;comment:上级分类ID:0为顶级;index"`
	Sort     int     `json:"sort" gorm:"not null;default:0;comment:排序:升序"`
}

func (Category) TableName() string { return "admin_categories" }
