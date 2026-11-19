package customer

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/dawnim/dawn-server/app/models"
)

// AppIDs 在执行查询前得到实际应用范围，计数、详情、消息和导出共用此边界。
func (m *Manager) AppIDs(ctx context.Context, adminID uint, action string) ([]uint, error) {
	if err := m.AuthorizeAdmin(ctx, adminID, action); err != nil {
		return nil, err
	}
	var user models.AdminUser
	if err := m.DB.WithContext(ctx).First(&user, adminID).Error; err != nil {
		return nil, err
	}
	var apps []models.SupportApp
	if err := m.DB.WithContext(ctx).Select("id", "config").Find(&apps).Error; err != nil {
		return nil, err
	}
	ids := []uint{}
	for _, app := range apps {
		var cfg Config
		if err := json.Unmarshal(app.Config, &cfg); err != nil {
			return nil, err
		}
		if user.Super || slices.Contains(cfg.AgentIDs, adminID) {
			ids = append(ids, app.ID)
		}
	}
	return ids, nil
}
