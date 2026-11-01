package models

type Post struct {
	Base
	Name   string `json:"name" gorm:"not null;size:100;comment:岗位名称" binding:"required,max=100"`
	Code   string `json:"code" gorm:"not null;size:100;uniqueIndex;comment:岗位唯一编码" binding:"required,max=100"`
	Sort   int    `json:"sort" gorm:"not null;default:0;comment:排序:升序"`
	Status int    `json:"status" gorm:"not null;type:smallint;default:1;comment:状态:1正常 2停用"`
	Remark string `json:"remark" gorm:"not null;comment:备注;size:500"`
}

func (Post) TableName() string { return "admin_posts" }
