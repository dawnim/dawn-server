package admin

import (
	"context"

	"github.com/dawnim/dawn-server/app/models"
	"gorm.io/gorm"
)

// InstallDefaults 仅在安装时补齐基础资料，不重置管理员或覆盖已编辑的数据。
func (a *App) InstallDefaults(ctx context.Context, username, password string) error {
	if err := a.CreateAdmin(username, password); err != nil {
		return err
	}
	return a.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		department := models.Department{Name: "Dawn", Status: models.Enabled}
		if err := db.Where("name = ? AND parent_id = ?", department.Name, 0).FirstOrCreate(&department).Error; err != nil {
			return err
		}
		post := models.Post{Name: "CEO", Code: "CEO", Status: models.Enabled}
		if err := db.Where("code = ?", post.Code).FirstOrCreate(&post).Error; err != nil {
			return err
		}
		var user models.User
		if err := db.Where("username = ?", username).First(&user).Error; err != nil {
			return err
		}
		if user.DepartmentID == 0 {
			if err := db.Model(&user).Update("department_id", department.ID).Error; err != nil {
				return err
			}
		}
		if err := db.Model(&user).Association("Posts").Append(&post); err != nil {
			return err
		}
		root := models.Category{Name: "全部分类", Sort: 100}
		if err := db.Where("name = ? AND parent_id = ?", root.Name, 0).FirstOrCreate(&root).Error; err != nil {
			return err
		}
		for i, name := range []string{"图片分类", "文件分类", "系统图片", "其他分类"} {
			category := models.Category{Name: name, ParentID: root.ID, Sort: i * 10}
			if err := db.Where("name = ? AND parent_id = ?", name, root.ID).FirstOrCreate(&category).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
