package models

type Attachment struct {
	Base
	Scope      string `json:"scope" gorm:"not null;size:80;default:admin;uniqueIndex:idx_admin_attachment_file;comment:文件引用来源:admin为后台附件库"`
	Name       string `json:"name" gorm:"not null;size:255;comment:附件名称" binding:"required,max=255"`
	FileID     uint   `json:"file_id" gorm:"not null;default:0;uniqueIndex:idx_admin_attachment_file;comment:文件ID"`
	CategoryID uint   `json:"category_id" gorm:"not null;default:0;index;comment:附件分类ID:0为未分类"`
	CreatedBy  uint   `json:"created_by" gorm:"not null;default:0;index;comment:上传管理员ID"`
	File       File   `json:"file" gorm:"foreignKey:FileID"`
}

func (Attachment) TableName() string { return "admin_attachments" }
