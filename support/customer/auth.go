package customer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/authsession"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Session struct {
	authsession.Record
	User  models.User `json:"user"`
	Token string      `json:"token,omitempty"`
}

// MarshalJSON只公开访客需要的字段，内部用户ID与会话记录ID不会发送到网站。
func (s Session) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Token     string             `json:"token,omitempty"`
		User      models.UserProfile `json:"user"`
		Verified  bool               `json:"verified"`
		ExpiresAt int64              `json:"expires_at"`
	}{s.Token, s.User.Profile(), s.Verified, s.ExpiresAt})
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) Authenticate(ctx context.Context, token string, appID uint) (Session, error) {
	var session Session
	if token == "" {
		return session, ErrUnauthenticated
	}
	record, err := m.Sessions.Authenticate(ctx, token, false)
	if errors.Is(err, authsession.ErrUnauthenticated) {
		return session, ErrUnauthenticated
	}
	if err != nil {
		return session, err
	}
	if record.AppID != appID {
		return session, ErrUnauthenticated
	}
	session.Record = record
	err = m.DB.WithContext(ctx).Where("status = ?", models.Enabled).First(&session.User, session.UserID).Error
	if err != nil || record.UserType != session.User.Type {
		return session, ErrUnauthenticated
	}
	return session, nil
}

func (m *Manager) NewSession(ctx context.Context, user models.User, appID uint, verified bool) (Session, error) {
	record, token, _, err := m.Sessions.Create(ctx, authsession.Record{UserID: user.ID, AppID: appID, Verified: verified, UserType: user.Type}, 180*24*time.Hour, 0)
	return Session{Record: record, User: user, Token: token}, err
}

// Register创建账号；传入本站游客会话时原地升级，保留用户编号与咨询记录。
func (m *Manager) Register(ctx context.Context, current Session, username, password, nickname string) (Session, error) {
	if len(username) < 3 || len(username) > 80 || len(password) < 6 || len(password) > 72 {
		return Session{}, ErrInvalid
	}
	if current.UserID != 0 && (current.AppID != 0 || current.User.Type != models.UserGuest) {
		return Session{}, ErrForbidden
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Session{}, err
	}
	u := models.User{Type: models.UserMember, Username: &username, Nickname: nickname, PasswordHash: string(hash)}
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if current.UserID == 0 {
			return tx.Create(&u).Error
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, current.UserID).Error; e != nil {
			return e
		}
		if u.Type != models.UserGuest {
			return ErrConflict
		}
		if e := tx.Model(&u).Updates(map[string]any{"username": username, "password_hash": string(hash), "nickname": nickname}).Error; e != nil {
			return e
		}
		return m.upgradeGuest(tx, &u)
	})
	if err != nil {
		return Session{}, err
	}
	u.Type = models.UserMember
	u.Username = &username
	u.Nickname = nickname
	if current.UserID != 0 {
		if err = m.Sessions.RevokeUsers(ctx, u.ID); err != nil {
			return Session{}, err
		}
	}
	return m.NewSession(ctx, u, 0, true)
}

func (m *Manager) Login(ctx context.Context, username, password string) (Session, error) {
	var u models.User
	if err := m.DB.WithContext(ctx).Where("username = ? AND status = ? AND type = ?", username, models.Enabled, models.UserMember).First(&u).Error; err != nil {
		return Session{}, ErrUnauthenticated
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return Session{}, ErrUnauthenticated
	}
	return m.NewSession(ctx, u, 0, true)
}

// IdentityTicket由本站后端签发，绑定用户类型，交换后立即失效。
func (m *Manager) IdentityTicket(ctx context.Context, userID, appID uint) (string, error) {
	_, cfg, _, err := m.App(ctx, appID)
	if err != nil {
		return "", err
	}
	if !cfg.AllowLocalUsers {
		return "", ErrForbidden
	}
	var user models.User
	if err = m.DB.WithContext(ctx).Where("status = ? AND type IN ?", models.Enabled, []string{models.UserGuest, models.UserMember}).First(&user, userID).Error; err != nil {
		return "", err
	}
	ticket := rand.Text()
	return ticket, m.Redis.Set(ctx, m.Key(constants.TempSupportTicket, TokenHash(ticket)), string(Raw(map[string]any{"app_id": appID, "user_id": userID, "user_type": user.Type})), time.Minute).Err()
}

type OpenInput struct {
	ConversationID uint           `json:"conversation_id"`
	EntryKey       string         `json:"entry_key"`
	Identity       string         `json:"identity"`
	Ticket         string         `json:"ticket"`
	Attributes     map[string]any `json:"attributes"`
	Observed       map[string]any `json:"observed"`
}

func (m *Manager) Open(ctx context.Context, publicKey, token string, input OpenInput) (Session, models.ChatConversation, error) {
	var conversation models.ChatConversation
	session, err := m.Identify(ctx, publicKey, token, input)
	if err != nil {
		return session, conversation, err
	}
	_, cfg, _, err := m.App(ctx, session.AppID)
	if err != nil {
		return session, conversation, err
	}
	if input.EntryKey == "" {
		input.EntryKey = "default"
	}

	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status := "ai"
		if !cfg.AIEnabled {
			status = "waiting"
		}
		conversation = models.ChatConversation{Type: "support", AppID: session.AppID, UserID: session.UserID, EntryKey: input.EntryKey, Identify: fmt.Sprintf("support:%d:%d", session.AppID, session.UserID), Status: status, Version: 1, Round: 1}
		var existing models.ChatConversation
		if input.ConversationID != 0 {
			result := tx.Where("id = ? AND app_id = ? AND user_id = ? AND type = ?", input.ConversationID, session.AppID, session.UserID, "support").Find(&existing)
			if result.Error != nil {
				return result.Error
			}
		}
		var e error
		if existing.ID == 0 {
			e = tx.Where("app_id = ? AND user_id = ? AND type = ? AND entry_key <> ?", session.AppID, session.UserID, "support", "telegram").Order("CASE WHEN status = 'human' THEN 0 ELSE 1 END, last_message_at DESC, id DESC").Take(&existing).Error
		}
		if e == nil {
			conversation = existing
		} else if e == gorm.ErrRecordNotFound {
			if e = tx.Create(&conversation).Error; e != nil {
				return e
			}
			if status == "waiting" {
				if e = tx.Create(&models.SupportSession{ConversationID: conversation.ID, Round: conversation.Round, Status: "waiting"}).Error; e != nil {
					return e
				}
			}
		} else {
			return e
		}
		member := models.ChatMember{ConversationID: conversation.ID, Realm: Realm(session.AppID), Subject: session.User.UniqueID}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error
	})
	if err == nil {
		conversation.SupportSession, err = m.CurrentSession(ctx, conversation.ID)
	}
	return session, conversation, err
}

// upgradeGuest在账号升级事务内停止旧游客任务，接待与评价保持原状态。
func (m *Manager) upgradeGuest(tx *gorm.DB, user *models.User) error {
	if err := tx.Model(user).Update("type", models.UserMember).Error; err != nil {
		return err
	}
	user.Type = models.UserMember
	var conversations []models.ChatConversation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND type = ?", user.ID, "support").Find(&conversations).Error; err != nil {
		return err
	}
	for _, conv := range conversations {
		if err := m.Cancel(tx, &conv, false, "用户身份已更新"); err != nil {
			return err
		}
		if err := m.receptionEvent(tx, &conv); err != nil {
			return err
		}
	}
	return nil
}
