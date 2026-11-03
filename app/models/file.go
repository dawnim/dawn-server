package models

import "github.com/dawnim/dawn-server/support/storage"

type File struct {
	Location  storage.Config `json:"-" gorm:"not null;serializer:json;type:text;comment:上传时的存储位置(JSON):驱动和路径参数,不含密钥"`
	ID        uint           `json:"id" gorm:"not null;primaryKey"`
	MD5       string         `json:"md5" gorm:"not null;size:32;index:idx_admin_file_lookup;comment:文件MD5摘要"`
	SHA256    string         `json:"sha256" gorm:"not null;size:64;uniqueIndex;comment:文件SHA-256摘要"`
	Size      int64          `json:"size" gorm:"not null;default:0;index:idx_admin_file_lookup;comment:文件大小(字节)"`
	Path      string         `json:"-" gorm:"not null;size:500;comment:存储路径"`
	MIME      string         `json:"mime" gorm:"not null;size:150;comment:文件媒体类型"`
	CreatedAt int64          `json:"created_at" gorm:"not null;autoCreateTime:milli"`
}

func (File) TableName() string { return "admin_files" }
