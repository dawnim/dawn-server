package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
)

func (a *App) initRealtime() {
	a.Realtime = realtime.New(a.Redis, a.Options.Name+":"+a.Options.Environment+":", a.Options.WSPath)
	a.Realtime.Origins = a.Options.AllowedOrigins
	a.Realtime.Authorize = a.Options.WSAuthorize
	a.Realtime.Validate = func(ctx context.Context, p realtime.Principal) error {
		if p.Realm != "admin" {
			if a.Options.WSValidate == nil {
				return i18n.Errorf("业务身份验证未注册")
			}
			return a.Options.WSValidate(ctx, p)
		}
		var session struct{ UserID uint }
		if err := a.DB.WithContext(ctx).Table("admin_sessions AS session").Select("session.user_id").
			Joins("JOIN admin_users AS u ON u.id = session.user_id AND u.deleted_at = 0 AND u.status = ?", models.Enabled).
			Where("session.id = ? AND session.expires_at > ?", p.SessionID, time.Now().UnixMilli()).Take(&session).Error; err != nil {
			return err
		}
		if p.Subject != strconv.FormatUint(uint64(session.UserID), 10) {
			return i18n.Errorf("身份与登录会话不符")
		}
		return nil
	}
}
