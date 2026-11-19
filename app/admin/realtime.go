package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dawnim/dawn-server/support/customer"
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
			if strings.HasPrefix(p.Realm, "support:") {
				return a.Customer.Validate(ctx, p)
			}
			if a.Options.WSValidate == nil {
				return i18n.Errorf("业务身份验证未注册")
			}
			return a.Options.WSValidate(ctx, p)
		}
		actor, err := a.ActorForSession(p.SessionID)
		if err != nil {
			return err
		}
		if p.Subject != strconv.FormatUint(uint64(actor.User.ID), 10) {
			return i18n.Errorf("身份与登录会话不符")
		}
		return nil
	}
	a.Realtime.AuthorizeEvent = func(ctx context.Context, p realtime.Principal, event realtime.Message) error {
		if !strings.HasPrefix(event.Type, "chat.") && !strings.HasPrefix(event.Type, "support.") {
			return nil
		}
		var envelope struct {
			ConversationID uint            `json:"conversation_id"`
			Visibility     string          `json:"visibility"`
			Version        uint            `json:"version"`
			Data           json.RawMessage `json:"data"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return customer.ErrForbidden
		}
		if len(envelope.Data) > 0 {
			if json.Unmarshal(envelope.Data, &envelope) != nil {
				return customer.ErrForbidden
			}
		}
		if envelope.ConversationID == 0 {
			return customer.ErrForbidden
		}
		var conv models.ChatConversation
		if err := a.DB.WithContext(ctx).First(&conv, envelope.ConversationID).Error; err != nil {
			return err
		}
		if event.Type == "chat.message.delta" && envelope.Version != conv.Version {
			return customer.ErrConflict
		}
		if conv.Type == "support" {
			if p.Realm == "admin" {
				id, _ := strconv.ParseUint(p.Subject, 10, 64)
				return a.Customer.AgentAccess(ctx, conv.AppID, uint(id), "view")
			}
			if envelope.Visibility != "" && envelope.Visibility != "public" {
				return customer.ErrForbidden
			}
			if p.Realm != customer.Realm(conv.AppID) {
				return customer.ErrForbidden
			}
			var owner models.User
			if err := a.DB.WithContext(ctx).Select("id").Where("id = ? AND unique_id = ?", conv.UserID, p.Subject).First(&owner).Error; err != nil {
				return customer.ErrForbidden
			}
			return a.Customer.Validate(ctx, p)
		}
		var member models.ChatMember
		return a.DB.WithContext(ctx).Where("conversation_id = ? AND realm = ? AND subject = ?", conv.ID, p.Realm, p.Subject).First(&member).Error
	}

}
