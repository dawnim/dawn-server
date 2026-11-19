// Package customer 提供网站和Telegram共用的客服应用、身份、接待和AI执行能力。
package customer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/authsession"
	"github.com/prismgo/framework/container"
	cc "github.com/prismgo/framework/contracts/container"
	"github.com/prismgo/framework/encryption"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var (
	ErrForbidden         = errors.New("无权访问此会话")
	ErrConflict          = errors.New("会话已更新，请刷新后重试")
	ErrClosureExpired    = errors.New("结束申请已失效，请刷新会话")
	ErrRatingUnavailable = errors.New("该次接待尚未结束")
	ErrRated             = errors.New("该次接待已经评价")
	ErrInvalid           = errors.New("客服参数无效")
	ErrUnauthenticated   = errors.New("用户会话已失效")
)

type Entry struct {
	Key      string   `json:"key"`
	Origins  []string `json:"origins"`
	Theme    string   `json:"theme"`
	Greeting string   `json:"greeting"`
}

type Config struct {
	Entries               []Entry    `json:"entries"`
	AgentIDs              []uint     `json:"agent_ids"`
	OfflineTimeoutMinutes *int       `json:"offline_timeout_minutes"`
	ClosingOfflineMinutes *int       `json:"closing_offline_minutes"`
	Capacity              int        `json:"capacity"`
	Greeting              string     `json:"greeting"`
	Instructions          string     `json:"instructions"`
	ModelProtocol         string     `json:"model_protocol"`
	ReasoningEffort       string     `json:"reasoning_effort"`
	MaxOutputTokens       int64      `json:"max_output_tokens"`
	Model                 string     `json:"model"`
	BaseURL               string     `json:"base_url"`
	EmbeddingURL          string     `json:"embedding_url"`
	EmbeddingModel        string     `json:"embedding_model"`
	EmbeddingDimensions   int        `json:"embedding_dimensions"`
	IndexProfile          string     `json:"index_profile"`
	IndexDriver           string     `json:"index_driver"`
	ActiveIndex           *IndexSpec `json:"active_index,omitempty"`
	AllowLocalUsers       bool       `json:"allow_local_users"`
	InputPreview          bool       `json:"input_preview"`
	AIEnabled             bool       `json:"ai_enabled"`
	TelegramEnabled       bool       `json:"telegram_enabled"`
	TelegramGroups        []int64    `json:"telegram_groups"`
	TelegramProactive     bool       `json:"telegram_proactive"`
	Tools                 []Tool     `json:"tools"`
	Skills                []Skill    `json:"skills"`
	ContextFields         []string   `json:"context_fields"`
}

type Secrets struct {
	EmbeddingAPIKey string                       `json:"embedding_api_key"`
	APIKey          string                       `json:"api_key"`
	IdentitySecret  string                       `json:"identity_secret"`
	TelegramToken   string                       `json:"telegram_token"`
	ToolHeaders     map[string]map[string]string `json:"tool_headers"`
}

type Manager struct {
	presenceFailed   atomic.Bool
	Storage          string
	DB               *gorm.DB
	Sessions         *authsession.Store
	Redis            redis.UniversalClient
	Hub              *realtime.Hub
	Prefix           string
	Wake             func(context.Context)
	AuthorizeAdmin   func(context.Context, uint, string) error
	Client           *http.Client
	TelegramURL      string
	mu               sync.RWMutex
	tools            map[string]ToolHandler
	queries          map[string]Query
	telegramHandlers []TelegramHandler
	retrievers       map[string]Retriever
}

// ManagerFrom 解析当前应用已注册的客服实例，不另建数据库或Redis连接。
func ManagerFrom(r cc.Resolver) (*Manager, error) {
	v, err := r.Make("dawn.customer")
	if err != nil {
		return nil, err
	}
	return v.(*Manager), nil
}
func Resolve() *Manager {
	m, err := container.Make[*Manager]("dawn.customer")
	if err != nil {
		panic(err)
	}
	return m
}

func New(db *gorm.DB, client redis.UniversalClient, hub *realtime.Hub, prefix string) (*Manager, error) {
	sessions, err := authsession.New("user")
	if err != nil {
		return nil, err
	}
	return &Manager{Sessions: sessions, DB: db, Redis: client, Hub: hub, Prefix: prefix, Client: &http.Client{Timeout: 30 * time.Second}, TelegramURL: "https://api.telegram.org", tools: map[string]ToolHandler{}, queries: map[string]Query{}, retrievers: map[string]Retriever{}}, nil
}

func Raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (m *Manager) App(ctx context.Context, id uint) (models.SupportApp, Config, Secrets, error) {
	var app models.SupportApp
	if err := m.DB.WithContext(ctx).First(&app, id).Error; err != nil {
		return app, Config{}, Secrets{}, err
	}
	var cfg Config
	if len(app.Config) > 0 {
		if err := json.Unmarshal(app.Config, &cfg); err != nil {
			return app, cfg, Secrets{}, err
		}
	}
	if cfg.ModelProtocol == "" {
		cfg.ModelProtocol = "responses"
	}
	var secrets Secrets
	if app.Secrets != "" {
		value, err := encryption.DecryptString(ctx, app.Secrets)
		if err != nil {
			return app, cfg, secrets, err
		}
		if err = json.Unmarshal([]byte(value), &secrets); err != nil {
			return app, cfg, secrets, err
		}
	}
	for i, skill := range cfg.Skills {
		if skill.Scope != "" {
			loaded, err := m.loadSkill(app.PublicKey, skill)
			if err != nil {
				return app, cfg, secrets, err
			}
			cfg.Skills[i] = loaded
		}
	}
	return app, cfg, secrets, nil
}

// CreateApp 建立独立AI身份，品牌资料只由应用配置提供。
func (m *Manager) CreateApp(ctx context.Context, app *models.SupportApp, secrets Secrets) error {
	return m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u := models.User{Type: models.UserAI, Nickname: app.Name}
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		app.AIUserID = u.ID
		if app.PublicKey == "" {
			app.PublicKey = rand.Text()
		}
		app.Revision = 1
		if secrets.TelegramToken != "" {
			prefix, _, ok := strings.Cut(secrets.TelegramToken, ":")
			id, e := strconv.ParseInt(prefix, 10, 64)
			if !ok || e != nil || id <= 0 {
				return ErrInvalid
			}
			app.TelegramBotID = &id
		}
		if secrets.APIKey != "" || secrets.EmbeddingAPIKey != "" || secrets.IdentitySecret != "" || secrets.TelegramToken != "" || len(secrets.ToolHeaders) > 0 {
			cipher, err := encryption.EncryptString(ctx, string(Raw(secrets)))
			if err != nil {
				return err
			}
			app.Secrets = cipher
		}
		return tx.Create(app).Error
	})
}

// AgentAccess 对HTTP、Telegram和后台任务应用相同的账号及应用范围。
func (m *Manager) AgentAccess(ctx context.Context, appID, adminID uint, action string) error {
	if m.AuthorizeAdmin == nil {
		return ErrForbidden
	}
	if err := m.AuthorizeAdmin(ctx, adminID, action); err != nil {
		return err
	}
	var user models.AdminUser
	if err := m.DB.WithContext(ctx).Where("status = ?", models.Enabled).First(&user, adminID).Error; err != nil {
		return ErrForbidden
	}
	_, cfg, _, err := m.App(ctx, appID)
	if err != nil {
		return err
	}
	if !user.Super && !slices.Contains(cfg.AgentIDs, adminID) {
		return ErrForbidden
	}
	return nil
}

func Realm(appID uint) string { return "support:" + strconv.FormatUint(uint64(appID), 10) }

func (m *Manager) Validate(ctx context.Context, p realtime.Principal) error {
	session, err := m.Sessions.Read(ctx, p.SessionID)
	if err != nil || session.ExpiresAt <= time.Now().UnixMilli() {
		return ErrUnauthenticated
	}
	if p.Realm != Realm(session.AppID) {
		return ErrUnauthenticated
	}
	var count int64
	if err := m.DB.WithContext(ctx).Model(&models.User{}).Where("id = ? AND unique_id = ? AND status = ? AND type = ?", session.UserID, p.Subject, models.Enabled, session.UserType).Where("EXISTS (SELECT 1 FROM support_apps WHERE id = ? AND status = ? AND deleted_at = 0)", session.AppID, models.Enabled).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrUnauthenticated
	}
	return nil
}

func (m *Manager) Key(d constants.Definition, args ...any) string {
	return m.Prefix + d.Format(args...)
}
