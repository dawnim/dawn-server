package customer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Driver      string         `json:"driver"`
	URL         string         `json:"url,omitempty"`
	RemoteName  string         `json:"remote_name,omitempty"`
	// RequireAccount要求已验证的注册用户，RequireIdentity仍允许已验证游客。
	RequireAccount  bool   `json:"require_account"`
	RequireIdentity bool   `json:"require_identity"`
	AllowGroups     bool   `json:"allow_groups"`
	Confirmation    bool   `json:"confirmation"`
	Skill           string `json:"skill,omitempty"`
}

// Query 是开发者注册的只读数据集，应用配置不能替换表、字段或归属条件。
type Query struct {
	Table, UserColumn, AppColumn, IdentityProvider string
	Columns, Filters                               []string
	MaxRows                                        int
}

func (m *Manager) RegisterQuery(name string, query Query) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries[name] = query
}

type Skill struct {
	Scope        string            `json:"scope,omitempty" yaml:"-"`
	Name         string            `json:"name" yaml:"name"`
	Description  string            `json:"description" yaml:"description"`
	AllowedTools string            `json:"allowed_tools" yaml:"allowed-tools"`
	Instructions string            `json:"instructions" yaml:"-"`
	References   map[string]string `json:"references,omitempty" yaml:"-"`
}

// ParseSkill 读取Agent Skills的SKILL.md；脚本仅作为资源，执行需显式注册工具。
func ParseSkill(content string) (Skill, error) {
	var s Skill
	header, body, ok := strings.Cut(strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "---\n"), "\n---\n")
	if !ok {
		return s, ErrInvalid
	}
	if err := yaml.Unmarshal([]byte(header), &s); err != nil {
		return s, err
	}
	if s.Name == "" || s.Description == "" || strings.ContainsAny(s.Name, "/\\.") {
		return s, ErrInvalid
	}
	s.Instructions = body
	return s, nil
}

type ToolActor struct {
	IdempotencyKey   string `json:"idempotency_key"`
	AppID            uint   `json:"app_id"`
	UserID           uint   `json:"user_id"`
	ExternalUserID   string `json:"external_user_id,omitempty"`
	IdentityProvider string `json:"identity_provider,omitempty"`
	Verified         bool   `json:"verified"`
	UserType         string `json:"user_type"`
	ConversationID   uint   `json:"conversation_id"`
	RunID            uint   `json:"run_id"`
	Public           bool   `json:"public"`
	Version          uint   `json:"version"`
	AdminID          uint   `json:"admin_id,omitempty"`
}

type ToolHandler func(context.Context, ToolActor, map[string]any) (any, error)

// RegisterTool 绑定真实业务能力；输入Schema与应用允许列表仍由运行时执行。
func (m *Manager) RegisterTool(name string, handler ToolHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[name] = handler
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func toolSchema(tool Tool) (*jsonschema.Schema, error) {
	var value any
	if err := json.Unmarshal(Raw(tool.Parameters), &value); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:dawn:"+tool.Name, value); err != nil {
		return nil, err
	}
	return compiler.Compile("urn:dawn:" + tool.Name)
}

// ValidateConfig 在配置写入边界检查工具契约，不在每层重复验证静态字段。
func ValidateConfig(cfg Config) error {
	if cfg.OfflineTimeoutMinutes != nil && *cfg.OfflineTimeoutMinutes < 0 || cfg.ClosingOfflineMinutes != nil && *cfg.ClosingOfflineMinutes < 0 {
		return ErrInvalid
	}
	if !slices.Contains([]string{"", "responses", "chat_completions", "anthropic"}, cfg.ModelProtocol) {
		return ErrInvalid
	}
	if cfg.MaxOutputTokens < 0 {
		return ErrInvalid
	}
	for _, id := range cfg.TelegramGroups {
		if id >= 0 {
			return ErrInvalid
		}
	}

	if cfg.BaseURL != "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ErrInvalid
		}
	}
	names := map[string]bool{"knowledge": true, "search_knowledge": true, "request_human": true, "read_skill_resource": true, "activate_skill": true}
	for _, t := range cfg.Tools {
		if !identifier.MatchString(t.Name) || names[t.Name] {
			return ErrInvalid
		}
		names[t.Name] = true
		switch t.Driver {
		case "go":
		case "sql":
		case "http", "mcp":
			u, err := url.Parse(t.URL)
			if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if _, err := toolSchema(t); err != nil {
			return err
		}
	}
	for _, s := range cfg.Skills {
		if s.Name == "" || s.Description == "" || strings.ContainsAny(s.Name, "/\\.") {
			return ErrInvalid
		}
		for _, name := range strings.Fields(s.AllowedTools) {
			if !names[name] {
				return ErrInvalid
			}
		}
	}
	return nil
}

// ExecuteTool 强制注入真实身份，SQL查询始终追加Scope；确认通过独立授权入口执行。
func (m *Manager) ExecuteTool(ctx context.Context, actor ToolActor, tool Tool, args map[string]any, confirmed bool) (result any, err error) {
	if actor.Public && !tool.AllowGroups {
		return nil, ErrForbidden
	}
	if (tool.RequireIdentity && !actor.Verified) || (tool.RequireAccount && (!actor.Verified || actor.UserType != models.UserMember)) {
		return nil, ErrUnauthenticated
	}
	schema, e := toolSchema(tool)
	if e != nil {
		return nil, e
	}
	if e = schema.Validate(args); e != nil {
		return nil, ErrInvalid
	}
	actor.IdempotencyKey = TokenHash(fmt.Sprintf("%d:%d:%s:%s", actor.ConversationID, actor.RunID, tool.Name, Raw(args)))
	audit := models.AIRun{Version: actor.Version, AppID: actor.AppID, ConversationID: actor.ConversationID, ParentID: actor.RunID, Kind: "tool", Status: "running", Input: Raw(map[string]any{"tool": tool.Name, "arguments": args, "actor": actor, "contract": TokenHash(string(Raw(tool)))})}
	if tool.Confirmation && !confirmed {
		audit.Status = "waiting"
		err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := tx.Create(&audit).Error; e != nil {
				return e
			}
			var conv models.ChatConversation
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conv, actor.ConversationID).Error; e != nil {
				return e
			}
			if conv.Version != actor.Version {
				return ErrConflict
			}
			visibility := "public"
			var root models.AIRun
			if actor.RunID != 0 {
				if e := tx.First(&root, actor.RunID).Error; e != nil {
					return e
				}
				if root.Kind == "draft" {
					visibility = "internal"
				}
			}
			message := models.ChatMessage{SenderRealm: "ai", SenderSubject: fmt.Sprint(actor.AppID), Type: "action", Content: tool.Description, Visibility: visibility, Payload: Raw(map[string]any{"action_id": audit.ID, "action_status": "waiting", "tool": tool.Name, "arguments": args})}
			if e := m.Message(tx, &conv, &message); e != nil {
				return e
			}
			return tx.Model(&audit).Update("reply_id", message.ID).Error
		})
		if err != nil {
			return nil, err
		}
		if m.Wake != nil {
			m.Wake(ctx)
		}
		return map[string]any{"confirmation_required": true, "run_id": audit.ID, "tool": tool.Name}, nil
	}
	if err = m.DB.WithContext(ctx).Create(&audit).Error; err != nil {
		return nil, err
	}
	defer func() {
		status := "completed"
		if err != nil {
			status = "failed"
		}
		message := ""
		if err != nil {
			message = "工具执行失败"
		}
		_ = m.DB.WithContext(context.WithoutCancel(ctx)).Model(&audit).Updates(map[string]any{"status": status, "output": Raw(result), "error": message}).Error
	}()
	if actor.RunID != 0 && !confirmed {
		var active int64
		if e = m.DB.WithContext(ctx).Model(&models.AIRun{}).Where("id = ? AND status = ?", actor.RunID, "running").Count(&active).Error; e != nil {
			return nil, e
		}
		if active != 1 {
			return nil, context.Canceled
		}
	}
	switch tool.Driver {
	case "go":
		m.mu.RLock()
		handler := m.tools[tool.Name]
		m.mu.RUnlock()
		if handler == nil {
			return nil, errors.New("业务工具未注册")
		}
		return handler(ctx, actor, args)
	case "sql":
		// 外部用户编号由已验证身份提供，模型不能覆盖归属条件。
		if !actor.Verified {
			return nil, ErrUnauthenticated
		}
		m.mu.RLock()
		query, ok := m.queries[tool.Name]
		m.mu.RUnlock()
		if !ok {
			return nil, ErrForbidden
		}
		owner := any(actor.UserID)
		if actor.IdentityProvider != "" {
			if query.IdentityProvider != actor.IdentityProvider || query.AppColumn == "" {
				return nil, ErrForbidden
			}
			owner = actor.ExternalUserID
		} else if query.IdentityProvider != "" {
			return nil, ErrForbidden
		}
		q := m.DB.WithContext(ctx).Table(query.Table).Select(query.Columns).Where(query.UserColumn+" = ?", owner)
		if query.AppColumn != "" {
			q = q.Where(query.AppColumn+" = ?", actor.AppID)
		}
		for _, column := range query.Filters {
			if value, ok := args[column]; ok {
				q = q.Where(column+" = ?", value)
			}
		}
		limit := query.MaxRows
		if limit <= 0 {
			limit = 20
		}
		var rows []map[string]any
		if e = q.Order("id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return nil, e
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		return map[string]any{"data": rows, "has_more": more, "queried_at": time.Now().UnixMilli()}, nil
	case "http":
		_, _, secrets, e := m.App(ctx, actor.AppID)
		if e != nil {
			return nil, e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, tool.URL, bytes.NewReader(Raw(map[string]any{"arguments": args, "actor": actor, "idempotency_key": actor.IdempotencyKey})))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		for key, value := range secrets.ToolHeaders[tool.Name] {
			req.Header.Set(key, value)
		}
		client := *m.Client
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
		resp, e := client.Do(req)
		if e != nil {
			return nil, errors.New("业务接口不可用")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("业务接口HTTP %d", resp.StatusCode)
		}
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
		return result, e
	case "mcp":
		return m.callMCP(ctx, actor, tool, args)
	}
	return nil, ErrInvalid
}

func AllowedTool(cfg Config, name string, activeSkills []string) (Tool, bool) {
	for _, t := range cfg.Tools {
		if t.Name != name {
			continue
		}
		if t.Skill == "" {
			return t, true
		}
		for _, s := range cfg.Skills {
			if s.Name == t.Skill && slices.Contains(activeSkills, s.Name) && slices.Contains(strings.Fields(s.AllowedTools), name) {
				return t, true
			}
		}
	}
	return Tool{}, false
}
