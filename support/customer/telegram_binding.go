package customer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TelegramBinding struct {
	AdminID    uint   `json:"admin_id"`
	UserID     uint   `json:"user_id"`
	TelegramID int64  `json:"telegram_id"`
	Username   string `json:"username"`
	Nickname   string `json:"nickname"`
	ExpiresAt  int64  `json:"expires_at"`
}

func (m *Manager) CreateTelegramBinding(ctx context.Context, adminID uint) (string, error) {
	code := rand.Text()
	binding := TelegramBinding{AdminID: adminID, ExpiresAt: time.Now().Add(constants.TempTelegramBinding.TTL).UnixMilli()}
	return code, m.Redis.Set(ctx, m.Key(constants.TempTelegramBinding, TokenHash(code)), string(Raw(binding)), constants.TempTelegramBinding.TTL).Err()
}
func (m *Manager) TelegramBinding(ctx context.Context, adminID uint, code string) (TelegramBinding, error) {
	var binding TelegramBinding
	value, err := m.Redis.Get(ctx, m.Key(constants.TempTelegramBinding, TokenHash(code))).Bytes()
	if err != nil {
		return binding, ErrInvalid
	}
	if json.Unmarshal(value, &binding) != nil || binding.AdminID != adminID {
		return binding, ErrForbidden
	}
	return binding, nil
}
func (m *Manager) ConfirmTelegramBinding(ctx context.Context, adminID uint, code string) error {
	binding, err := m.TelegramBinding(ctx, adminID, code)
	if err != nil {
		return err
	}
	if binding.UserID == 0 {
		return ErrConflict
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admin models.AdminUser
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&admin, adminID).Error; e != nil {
			return e
		}
		if admin.UserID != nil && *admin.UserID != binding.UserID {
			return ErrConflict
		}
		var user models.User
		if e := tx.Where("telegram_id = ?", binding.TelegramID).First(&user, binding.UserID).Error; e != nil {
			return e
		}
		return tx.Model(&admin).Update("user_id", user.ID).Error
	})
	if err == nil {
		err = m.Redis.Del(ctx, m.Key(constants.TempTelegramBinding, TokenHash(code))).Err()
	}
	return err
}
