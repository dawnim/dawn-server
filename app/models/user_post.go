package models

// UserPost 用户与岗位关联.
type UserPost struct {
	UserID uint `gorm:"not null;primaryKey;comment:管理员ID"`
	PostID uint `gorm:"not null;primaryKey;index;comment:岗位ID"`
}

func (UserPost) TableName() string { return "admin_user_posts" }
