package customer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// identityClaims验证应用签名、用途与一次性请求编号。
func (m *Manager) identityClaims(ctx context.Context, app models.SupportApp, secret, proof, scope string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	if secret == "" {
		return nil, ErrUnauthenticated
	}
	_, err := jwt.ParseWithClaims(proof, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithAudience(app.PublicKey))
	if err != nil {
		return nil, ErrUnauthenticated
	}
	sub, _ := claims.GetSubject()
	issuer, _ := claims.GetIssuer()
	jti, _ := claims["jti"].(string)
	purpose, _ := claims["scope"].(string)
	if sub == "" || issuer == "" || issuer == "__local" || jti == "" || len(sub) > 180 || len(issuer) > 100 || purpose != scope {
		return nil, ErrUnauthenticated
	}
	expires, _ := claims.GetExpirationTime()
	fresh, err := m.Redis.SetNX(ctx, m.Key(constants.TempIdentityProof, app.ID, TokenHash(issuer+":"+jti)), 1, time.Until(expires.Time)).Result()
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, ErrUnauthenticated
	}
	return claims, nil
}

// Identify建立或恢复身份，不创建咨询，也不排队；外部游客与注册用户分别处理。
func (m *Manager) Identify(ctx context.Context, publicKey, token string, input OpenInput) (Session, error) {
	var app models.SupportApp
	if err := m.DB.WithContext(ctx).Where("public_key = ? AND status = ?", publicKey, models.Enabled).First(&app).Error; err != nil {
		return Session{}, err
	}
	_, cfg, secrets, err := m.App(ctx, app.ID)
	if err != nil {
		return Session{}, err
	}
	if input.EntryKey != "" && input.EntryKey != "default" && !slices.ContainsFunc(cfg.Entries, func(e Entry) bool { return e.Key == input.EntryKey }) {
		return Session{}, ErrInvalid
	}
	previous, authErr := m.Authenticate(ctx, token, app.ID)
	if token != "" && authErr != nil {
		if !errors.Is(authErr, ErrUnauthenticated) {
			return Session{}, authErr
		}
		// 新的后端证明可以恢复刚升级的用户，过期凭证不能参与历史归并。
		if input.Ticket == "" && input.Identity == "" {
			return Session{}, authErr
		}
		previous = Session{}
	}
	var target models.User
	var issuer, subject string
	if input.Ticket != "" {
		raw, e := m.Redis.GetDel(ctx, m.Key(constants.TempSupportTicket, TokenHash(input.Ticket))).Bytes()
		if e != nil {
			return Session{}, ErrUnauthenticated
		}
		var ticket struct {
			AppID    uint   `json:"app_id"`
			UserID   uint   `json:"user_id"`
			UserType string `json:"user_type"`
		}
		if json.Unmarshal(raw, &ticket) != nil || ticket.AppID != app.ID {
			return Session{}, ErrUnauthenticated
		}
		if e = m.DB.WithContext(ctx).Where("status = ? AND type = ?", models.Enabled, ticket.UserType).First(&target, ticket.UserID).Error; e != nil {
			return Session{}, ErrUnauthenticated
		}
	} else if input.Identity != "" {
		claims, e := m.identityClaims(ctx, app, secrets.IdentitySecret, input.Identity, "")
		if e != nil {
			return Session{}, e
		}
		issuer, _ = claims.GetIssuer()
		subject, _ = claims.GetSubject()
		kind, _ := claims["type"].(string)
		if kind == "" {
			kind = models.UserMember
		}
		if kind != models.UserGuest && kind != models.UserMember {
			return Session{}, ErrInvalid
		}
		e = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var traits models.UserTraits
			e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("app_id = ? AND identity_provider = ? AND external_user_id = ?", app.ID, issuer, subject).Take(&traits).Error
			if e == nil {
				if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ?", models.Enabled).First(&target, traits.UserID).Error; e != nil {
					return e
				}
				if target.Type == models.UserMember && kind == models.UserGuest {
					return ErrUnauthenticated
				}
				if target.Type != kind {
					if e = m.upgradeGuest(tx, &target); e != nil {
						return e
					}
					target.Type = kind
				}
			} else if errors.Is(e, gorm.ErrRecordNotFound) {
				// 有当前客服游客的持有证明时直接绑定，避免再创建一个客服用户。
				if previous.UserID != 0 && previous.User.Type == models.UserGuest {
					var count int64
					if e = tx.Model(&models.UserTraits{}).Where("user_id = ? AND identity_provider IS NOT NULL", previous.UserID).Count(&count).Error; e != nil {
						return e
					}
					if count == 0 {
						target = previous.User
					}
				}
				if target.ID == 0 {
					target = models.User{Type: kind}
					if e = tx.Create(&target).Error; e != nil {
						return e
					}
				} else if kind == models.UserMember {
					if e = m.upgradeGuest(tx, &target); e != nil {
						return e
					}
				}
				target.Type = kind
				traits = models.UserTraits{AppID: app.ID, UserID: target.ID, IdentityProvider: &issuer, ExternalUserID: &subject}
				if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "app_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"identity_provider", "external_user_id"})}).Create(&traits).Error; e != nil {
					return e
				}
			} else {
				return e
			}
			if profile, ok := claims["user"].(map[string]any); ok {
				values := map[string]any{}
				for _, key := range []string{"nickname", "avatar", "email", "mobile"} {
					if value, ok := profile[key].(string); ok {
						values[key] = value
					}
				}
				if len(values) > 0 {
					if e = tx.Model(&target).Updates(values).Error; e != nil {
						return e
					}
				}
			}
			return tx.First(&target, target.ID).Error
		})
		if e != nil {
			return Session{}, e
		}
	}
	session := previous
	if target.ID != 0 && target.ID == previous.UserID && target.Type == previous.User.Type && previous.Verified {
		session.User = target
	} else if target.ID != 0 {
		session, err = m.NewSession(ctx, target, app.ID, true)
	} else if authErr != nil {
		target = models.User{Type: models.UserGuest}
		if err = m.DB.WithContext(ctx).Create(&target).Error; err == nil {
			session, err = m.NewSession(ctx, target, app.ID, false)
		}
	}
	if err != nil {
		return Session{}, err
	}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if previous.UserID != 0 && previous.User.Type == models.UserGuest && previous.UserID != session.UserID {
			var conversations []models.ChatConversation
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("app_id = ? AND user_id = ? AND type = ?", app.ID, previous.UserID, "support").Find(&conversations).Error; e != nil {
				return e
			}
			for _, conv := range conversations {
				if e := m.Cancel(tx, &conv, false, "用户身份已更新"); e != nil {
					return e
				}
				conv.UserID = session.UserID
				if e := tx.Model(&conv).Updates(map[string]any{"user_id": session.UserID, "identify": fmt.Sprintf("support:%d:%d:%d", app.ID, session.UserID, conv.ID)}).Error; e != nil {
					return e
				}
				if e := tx.Model(&models.ChatMember{}).Where("conversation_id = ? AND realm = ? AND subject = ?", conv.ID, Realm(app.ID), previous.User.UniqueID).Update("subject", session.User.UniqueID).Error; e != nil {
					return e
				}
				if e := m.receptionEvent(tx, &conv); e != nil {
					return e
				}
			}
		}
		var traits models.UserTraits
		e := tx.Where("app_id = ? AND user_id = ?", app.ID, session.UserID).Take(&traits).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		attrs, observed := map[string]any{}, map[string]any{}
		if previous.UserID != 0 && previous.User.Type == models.UserGuest && previous.UserID != session.UserID {
			var prior models.UserTraits
			e := tx.Where("app_id = ? AND user_id = ?", app.ID, previous.UserID).Take(&prior).Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			_ = json.Unmarshal(prior.Attributes, &attrs)
			_ = json.Unmarshal(prior.Observed, &observed)
			if traits.Remark == "" {
				traits.Remark = prior.Remark
			}
			if prior.FirstSeenAt > 0 && (traits.FirstSeenAt == 0 || prior.FirstSeenAt < traits.FirstSeenAt) {
				traits.FirstSeenAt = prior.FirstSeenAt
			}
		}
		_ = json.Unmarshal(traits.Attributes, &attrs)
		_ = json.Unmarshal(traits.Observed, &observed)
		for k, v := range input.Attributes {
			attrs[k] = v
		}
		for k, v := range input.Observed {
			observed[k] = v
		}
		traits.AppID = app.ID
		traits.UserID = session.UserID
		traits.Attributes = Raw(attrs)
		traits.Observed = Raw(observed)
		traits.LastSeenAt = time.Now().UnixMilli()
		if session.Verified {
			traits.VerifiedAt = time.Now().UnixMilli()
		}
		return tx.Save(&traits).Error
	})
	if err == nil && previous.ID != "" && session.ID != previous.ID {
		err = m.Sessions.Revoke(ctx, previous.ID)
	}
	if err == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return session, err
}

// VisitorTicket将当前游客转换为短期反向票据，不能用来登录注册账号。
func (m *Manager) VisitorTicket(ctx context.Context, s Session) (string, error) {
	if s.User.Type != models.UserGuest {
		return "", ErrForbidden
	}
	ticket := rand.Text()
	raw := Raw(map[string]any{"app_id": s.AppID, "user_id": s.UserID, "session_id": s.ID})
	return ticket, m.Redis.Set(ctx, m.Key(constants.TempSupportVisitorTicket, TokenHash(ticket)), string(raw), time.Minute).Err()
}

// ClaimVisitor验证反向票据；同一接收方可幂等重试，不同接收方不能重复认领。
func (m *Manager) ClaimVisitor(ctx context.Context, appID uint, ticket, receiver string) (models.User, error) {
	var user models.User
	if receiver == "" {
		return user, ErrForbidden
	}
	raw, err := m.Redis.Eval(ctx, `local v=redis.call('GET',KEYS[1]);if not v then return '' end;local t=cjson.decode(v);if tostring(t.app_id)~=ARGV[2] then return '' end;if t.receiver and t.receiver~=ARGV[1] then return '' end;t.receiver=ARGV[1];redis.call('SET',KEYS[1],cjson.encode(t),'KEEPTTL');return cjson.encode(t)`, []string{m.Key(constants.TempSupportVisitorTicket, TokenHash(ticket))}, receiver, appID).Text()
	if err != nil {
		return user, err
	}
	var proof struct {
		AppID     uint   `json:"app_id"`
		UserID    uint   `json:"user_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(raw), &proof) != nil || proof.AppID != appID {
		return user, ErrUnauthenticated
	}
	session, err := m.Sessions.Read(ctx, proof.SessionID)
	if err != nil || session.UserID != proof.UserID || session.AppID != appID || session.ExpiresAt <= time.Now().UnixMilli() {
		return user, ErrUnauthenticated
	}
	err = m.DB.WithContext(ctx).Where("type = ? AND status = ?", models.UserGuest, models.Enabled).First(&user, proof.UserID).Error
	if err != nil {
		return models.User{}, ErrUnauthenticated
	}
	return user, nil
}

// LocalIdentity供共用users表的网站建立游客会话，并签发客服身份票据。
func (m *Manager) LocalIdentity(ctx context.Context, appID uint, token, visitorTicket string) (Session, string, error) {
	_, cfg, _, err := m.App(ctx, appID)
	if err != nil {
		return Session{}, "", err
	}
	if !cfg.AllowLocalUsers {
		return Session{}, "", ErrForbidden
	}
	var s Session
	if token != "" {
		s, err = m.Authenticate(ctx, token, 0)
	} else {
		var user models.User
		if visitorTicket != "" {
			user, err = m.ClaimVisitor(ctx, appID, visitorTicket, "__local")
		} else {
			user = models.User{Type: models.UserGuest}
			err = m.DB.WithContext(ctx).Create(&user).Error
		}
		if err == nil {
			s, err = m.NewSession(ctx, user, 0, false)
		}
	}
	if err != nil {
		return Session{}, "", err
	}
	ticket, err := m.IdentityTicket(ctx, s.UserID, appID)
	return s, ticket, err
}

// ExchangeVisitor供外部网站后端交换游客票据，只返回该应用的公开编号和外部映射。
func (m *Manager) ExchangeVisitor(ctx context.Context, publicKey, ticket, proof string) (map[string]any, error) {
	var app models.SupportApp
	if err := m.DB.WithContext(ctx).Where("public_key = ? AND status = ?", publicKey, models.Enabled).First(&app).Error; err != nil {
		return nil, err
	}
	_, _, secrets, err := m.App(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	claims, err := m.identityClaims(ctx, app, secrets.IdentitySecret, proof, "visitor_exchange")
	if err != nil {
		return nil, err
	}
	issuer, _ := claims.GetIssuer()
	user, err := m.ClaimVisitor(ctx, app.ID, ticket, issuer)
	if err != nil {
		return nil, err
	}
	var traits models.UserTraits
	if err = m.DB.WithContext(ctx).Where("app_id = ? AND user_id = ?", app.ID, user.ID).Take(&traits).Error; err != nil {
		return nil, err
	}
	result := map[string]any{"visitor_id": user.UniqueID, "type": models.UserGuest}
	if traits.IdentityProvider != nil && *traits.IdentityProvider == issuer && traits.ExternalUserID != nil {
		result["external_user_id"] = *traits.ExternalUserID
	}
	return result, nil
}
