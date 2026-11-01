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
		var session models.Session
		if err := a.DB.WithContext(ctx).Where("id = ? AND expires_at > ?", p.SessionID, time.Now().UnixMilli()).First(&session).Error; err != nil {
			return err
		}
		if p.Subject != strconv.FormatUint(uint64(session.UserID), 10) {
			return i18n.Errorf("身份与登录会话不符")
		}
		var user models.User
		return a.DB.WithContext(ctx).Where("status = ?", models.Enabled).First(&user, session.UserID).Error
	}
}
