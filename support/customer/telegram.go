package customer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/authsession"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/go-telegram/bot"
	tg "github.com/go-telegram/bot/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TelegramProfile struct {
	ID                                    int64
	Username, FirstName, LastName, Locale string
}

// TelegramEvent 统一扩展事件字段，Raw保留需要接入其他Bot API能力的原始更新。
type TelegramEvent struct {
	AppID     uint            `json:"app_id"`
	User      models.User     `json:"user"`
	ChatID    int64           `json:"chat_id"`
	ThreadID  int             `json:"thread_id"`
	MessageID int             `json:"message_id"`
	Text      string          `json:"text"`
	Raw       json.RawMessage `json:"raw"`
}
type TelegramHandler func(context.Context, TelegramEvent) (bool, error)

func (m *Manager) RegisterTelegramHandler(handler TelegramHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.telegramHandlers = append(m.telegramHandlers, handler)
}

// TelegramGroup 是机器人实际收到更新的群组，仅用于后台候选选择。
type TelegramGroup struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type telegramContext struct {
	ChatID    int64  `json:"chat_id"`
	ThreadID  int    `json:"thread_id"`
	MessageID int    `json:"message_id"`
	Locale    string `json:"locale"`
	Channel   string `json:"channel"`
}
type telegramDelivery struct {
	telegramContext
	Text      string       `json:"text"`
	Experts   []uint       `json:"experts,omitempty"`
	ParseMode tg.ParseMode `json:"parse_mode,omitempty"`
}

// TelegramUser 根据Telegram可信数字ID查找或自动注册用户，用户名只作为可变资料。
func (m *Manager) TelegramUser(ctx context.Context, profile TelegramProfile) (models.User, error) {
	var user models.User
	err := m.DB.WithContext(ctx).Where("telegram_id = ?", profile.ID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user = models.User{Type: models.UserMember, TelegramID: &profile.ID, TelegramUsername: profile.Username, Nickname: strings.TrimSpace(profile.FirstName + " " + profile.LastName), Locale: profile.Locale}
		if user.Locale == "" {
			user.Locale = "en"
		}
		err = m.DB.WithContext(ctx).Create(&user).Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			err = m.DB.WithContext(ctx).Where("telegram_id = ?", profile.ID).First(&user).Error
		}
	} else if err == nil {
		err = m.DB.WithContext(ctx).Model(&user).Update("telegram_username", profile.Username).Error
		user.TelegramUsername = profile.Username
	}
	if err == nil && user.Status != models.Enabled {
		return user, ErrForbidden
	}
	return user, err
}

// TelegramBot 提供官方SDK客户端；调用方复用当前应用的密钥和HTTP资源，不输出带Token的URL。
func (m *Manager) TelegramBot(ctx context.Context, appID uint) (*bot.Bot, error) {
	_, _, secrets, err := m.App(ctx, appID)
	if err != nil {
		return nil, err
	}
	return bot.New(secrets.TelegramToken, bot.WithSkipGetMe(), bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "my_chat_member"}), bot.WithServerURL(m.TelegramURL), bot.WithHTTPClient(20*time.Second, m.Client), bot.WithErrorsHandler(func(err error) {
		slog.Warn("Telegram请求失败", "app_id", appID, "error_type", fmt.Sprintf("%T", err))
	}))
}

// telegramTransport 在SDK确认上游offset之前持久化整批更新，SDK内部缓冲不会成为唯一消息副本。
type telegramTransport struct {
	manager *Manager
	appID   uint
	base    http.RoundTripper
}

func (t telegramTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(request.URL.Path, "/getUpdates") || response.StatusCode != 200 {
		return response, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, ErrInvalid
	}
	var payload struct {
		OK     bool              `json:"ok"`
		Result []json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload.OK && len(payload.Result) > 0 {
		err = t.manager.DB.WithContext(request.Context()).Transaction(func(tx *gorm.DB) error {
			for _, raw := range payload.Result {
				var update struct {
					ID int64 `json:"update_id"`
				}
				if err := json.Unmarshal(raw, &update); err != nil {
					return err
				}
				key := fmt.Sprintf("telegram:update:%d:%d", t.appID, update.ID)
				run := models.AIRun{AppID: t.appID, Kind: "telegram_update", Input: raw, RequestKey: &key}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}

// RunTelegram 在现有WS进程中管理机器人；每个应用拥有可取消的goroutine，Redis租约避免多实例重复轮询。
func (m *Manager) RunTelegram(ctx context.Context) {
	type running struct {
		cancel   context.CancelFunc
		done     chan struct{}
		revision uint
	}
	active := map[uint]running{}
	defer func() {
		for _, entry := range active {
			entry.cancel()
		}
		for _, entry := range active {
			<-entry.done
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		var apps []models.SupportApp
		if m.DB.WithContext(ctx).Where("status = ? AND telegram_bot_id IS NOT NULL", models.Enabled).Find(&apps).Error == nil {
			wanted := map[uint]bool{}
			for _, app := range apps {
				wanted[app.ID] = true
				if previous, ok := active[app.ID]; ok {
					select {
					case <-previous.done:
						delete(active, app.ID)
					default:
						if previous.revision == app.Revision {
							continue
						}
						previous.cancel()
						<-previous.done
						delete(active, app.ID)
					}
				}
				_, cfg, secrets, err := m.App(ctx, app.ID)
				if err != nil || secrets.TelegramToken == "" || !cfg.TelegramEnabled {
					continue
				}
				botCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				active[app.ID] = running{cancel: cancel, done: done, revision: app.Revision}
				go func() { defer close(done); defer cancel(); m.runTelegramBot(botCtx, app, secrets.TelegramToken) }()
			}
			for id, entry := range active {
				if !wanted[id] {
					entry.cancel()
					<-entry.done
					delete(active, id)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) runTelegramBot(ctx context.Context, app models.SupportApp, token string) {
	owner := rand.Text()
	key := m.Key(constants.TempTelegramLease, app.ID)
	acquired, err := m.Redis.SetNX(ctx, key, owner, constants.TempTelegramLease.TTL).Result()
	if err != nil || !acquired {
		return
	}
	defer m.Redis.Eval(context.WithoutCancel(ctx), `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{key}, owner)
	client := *m.Client
	transport := m.Client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = telegramTransport{manager: m, appID: app.ID, base: transport}
	instance, err := bot.New(token, bot.WithSkipGetMe(), bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "my_chat_member"}), bot.WithServerURL(m.TelegramURL), bot.WithHTTPClient(20*time.Second, &client), bot.WithNotAsyncHandlers(), bot.WithUpdatesChannelCap(1), bot.WithDefaultHandler(func(context.Context, *bot.Bot, *tg.Update) {}), bot.WithErrorsHandler(func(err error) {
		slog.Warn("Telegram轮询等待重试", "app_id", app.ID, "error_type", fmt.Sprintf("%T", err))
	}))
	if err != nil {
		return
	}
	me, err := instance.GetMe(ctx)
	if err != nil {
		return
	}
	if err = m.DB.WithContext(ctx).Model(&app).Update("telegram_bot_id", me.ID).Error; err != nil {
		return
	}
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { instance.Start(pollCtx) })
	defer wg.Wait()
	defer cancel()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	work := time.NewTicker(300 * time.Millisecond)
	defer work.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			renewed, e := m.Redis.Eval(ctx, `if redis.call('GET',KEYS[1])==ARGV[1] then redis.call('EXPIRE',KEYS[1],60);return 1 end return 0`, []string{key}, owner).Int()
			if e != nil || renewed != 1 {
				return
			}
		case <-work.C:
			if err = m.telegramTask(ctx, app.ID, instance, me); err != nil && ctx.Err() == nil {
				slog.Warn("Telegram任务等待重试", "app_id", app.ID, "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}
func (m *Manager) telegramTask(ctx context.Context, appID uint, instance *bot.Bot, me *tg.User) error {
	var run models.AIRun
	err := m.DB.WithContext(ctx).Where("app_id = ? AND kind IN ? AND status IN ? AND available_at <= ?", appID, []string{"telegram_update", "telegram_send"}, []string{"queued", "running"}, time.Now().UnixMilli()).Order("id").Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var delivery telegramDelivery
	if run.Kind == "telegram_send" {
		if err = json.Unmarshal(run.Input, &delivery); err != nil {
			return err
		}
		if delivery.ChatID < 0 {
			ok, e := m.Redis.SetNX(ctx, m.Key(constants.TempTelegramSend, appID, delivery.ChatID), 1, 3*time.Second).Result()
			if e != nil {
				return e
			}
			if !ok {
				return m.DB.WithContext(ctx).Model(&run).Update("available_at", time.Now().Add(3*time.Second).UnixMilli()).Error
			}
		}
	}
	lease := rand.Text()
	claim := m.DB.WithContext(ctx).Model(&run).Where("status IN ? AND available_at <= ?", []string{"queued", "running"}, time.Now().UnixMilli()).Updates(map[string]any{"status": "running", "lease_token": lease, "attempt": gorm.Expr("attempt + 1"), "available_at": time.Now().Add(2 * time.Minute).UnixMilli()})
	if claim.Error != nil || claim.RowsAffected == 0 {
		return claim.Error
	}
	if run.Kind == "telegram_update" {
		var update tg.Update
		if err = json.Unmarshal(run.Input, &update); err == nil {
			err = m.receiveTelegram(ctx, appID, instance, me, &update)
		}
	} else {
		text := delivery.Text
		if len(delivery.Experts) > 0 && delivery.ChatID < 0 {
			for _, id := range delivery.Experts {
				var user models.User
				e := m.DB.WithContext(ctx).Model(&models.User{}).Where("users.id = ? AND users.status = ? AND users.telegram_id > 0", id, models.Enabled).First(&user).Error
				if e != nil {
					continue
				}
				member, e := instance.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: delivery.ChatID, UserID: *user.TelegramID})
				if e != nil || member.Type == tg.ChatMemberTypeLeft || member.Type == tg.ChatMemberTypeBanned {
					continue
				}
				if delivery.ParseMode == "" {
					text = html.EscapeString(text)
					delivery.ParseMode = tg.ParseModeHTML
				}
				text += "\n<a href=\"tg://user?id=" + strconv.FormatInt(*user.TelegramID, 10) + "\">" + html.EscapeString(user.Nickname) + "</a>"
			}
		}
		parts := []rune(text)
		for start := 0; start < len(parts); start += 1900 {
			params := &bot.SendMessageParams{ChatID: delivery.ChatID, MessageThreadID: delivery.ThreadID, Text: string(parts[start:min(start+1900, len(parts))]), ParseMode: delivery.ParseMode, LinkPreviewOptions: &tg.LinkPreviewOptions{IsDisabled: bot.True()}}
			if delivery.MessageID != 0 {
				params.ReplyParameters = &tg.ReplyParameters{MessageID: delivery.MessageID, AllowSendingWithoutReply: true}
			}
			var sent *tg.Message
			sent, err = instance.SendMessage(ctx, params)
			if err != nil {
				break
			}
			if run.ReplyID != 0 && start == 0 {
				external := fmt.Sprintf("telegram:%d:%d:%d", appID, delivery.ChatID, sent.ID)
				err = m.DB.WithContext(ctx).Model(&models.ChatMessage{}).Where("id = ?", run.ReplyID).Update("external_id", external).Error
				if err != nil {
					break
				}
			}
		}
	}
	values := map[string]any{"status": "completed", "error": ""}
	if err != nil {
		values = map[string]any{"status": "queued", "available_at": time.Now().Add(time.Duration(min(60, 1<<min(run.Attempt+1, 6))) * time.Second).UnixMilli(), "error": "Telegram处理失败"}
		if run.Attempt >= 9 || errors.Is(err, ErrInvalid) || errors.Is(err, ErrForbidden) {
			values["status"] = "failed"
		}
	}
	saved := m.DB.WithContext(context.WithoutCancel(ctx)).Model(&run).Where("lease_token = ? AND status = ?", lease, "running").Updates(values).Error
	return errors.Join(err, saved)
}

func (m *Manager) receiveTelegram(ctx context.Context, appID uint, instance *bot.Bot, me *tg.User, update *tg.Update) error {
	var chat tg.Chat
	if update.Message != nil {
		chat = update.Message.Chat
	}
	if update.MyChatMember != nil {
		chat = update.MyChatMember.Chat
	}
	if chat.ID < 0 {
		key := m.Key(constants.TempTelegramGroups, appID, me.ID)
		pipe := m.Redis.TxPipeline()
		pipe.HSet(ctx, key, strconv.FormatInt(chat.ID, 10), string(Raw(TelegramGroup{ID: chat.ID, Title: chat.Title})))
		pipe.Expire(ctx, key, constants.TempTelegramGroups.TTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}
	message := update.Message
	if message == nil || message.From == nil || message.From.IsBot || message.Text == "" {
		return nil
	}
	_, cfg, _, err := m.App(ctx, appID)
	if err != nil {
		return err
	}
	if message.Chat.ID < 0 && !slices.Contains(cfg.TelegramGroups, message.Chat.ID) {
		return nil
	}
	user, err := m.TelegramUser(ctx, TelegramProfile{ID: message.From.ID, Username: message.From.Username, FirstName: message.From.FirstName, LastName: message.From.LastName, Locale: message.From.LanguageCode})
	if err != nil {
		return err
	}
	locale := "en"
	if strings.HasPrefix(user.Locale, "zh") {
		locale = "zh"
	}
	if message.Chat.ID > 0 && (strings.HasPrefix(message.Text, "/start bind_") || strings.HasPrefix(message.Text, "/bind ")) {
		code := strings.TrimPrefix(strings.TrimPrefix(message.Text, "/start bind_"), "/bind ")
		key := m.Key(constants.TempTelegramBinding, TokenHash(code))
		value, e := m.Redis.Get(ctx, key).Bytes()
		if e != nil {
			return ErrInvalid
		}
		var binding TelegramBinding
		if json.Unmarshal(value, &binding) != nil {
			return ErrInvalid
		}
		if binding.UserID != 0 && binding.UserID != user.ID {
			return ErrForbidden
		}
		binding.UserID = user.ID
		binding.TelegramID = message.From.ID
		binding.Username = message.From.Username
		binding.Nickname = user.Nickname
		if e = m.Redis.SetArgs(ctx, key, string(Raw(binding)), redis.SetArgs{Mode: "XX", KeepTTL: true}).Err(); e != nil {
			return e
		}
		return m.enqueueTelegram(ctx, appID, 0, "binding:"+strconv.FormatInt(update.ID, 10), telegramDelivery{telegramContext: telegramContext{ChatID: message.Chat.ID, MessageID: message.ID}, Text: i18n.Text(locale, "Telegram身份已验证，请返回后台确认绑定")})
	}
	m.mu.RLock()
	handlers := slices.Clone(m.telegramHandlers)
	m.mu.RUnlock()
	for _, handler := range handlers {
		handled, e := handler(ctx, TelegramEvent{AppID: appID, User: user, ChatID: message.Chat.ID, ThreadID: message.MessageThreadID, MessageID: message.ID, Text: message.Text, Raw: Raw(update)})
		if e != nil || handled {
			return e
		}
	}
	external := fmt.Sprintf("telegram:%d:%d:%d", appID, message.Chat.ID, message.ID)
	if message.ReplyToMessage != nil {
		var adminUser models.AdminUser
		if m.DB.WithContext(ctx).Where("user_id = ? AND status = ?", user.ID, models.Enabled).First(&adminUser).Error == nil {
			var replied models.ChatMessage
			replyExternal := fmt.Sprintf("telegram:%d:%d:%d", appID, message.Chat.ID, message.ReplyToMessage.ID)
			if m.DB.WithContext(ctx).Where("external_id = ?", replyExternal).First(&replied).Error == nil {
				conv, e := m.Conversation(ctx, replied.ConversationID, Session{}, adminUser.ID, "reply")
				if e != nil {
					return e
				}
				if conv.Status == "ai" || conv.Status == "closed" {
					conv, e = m.Reception(ctx, conv.ID, Session{}, adminUser.ID, 0, conv.Version, "human")
					if e != nil {
						return e
					}
				}
				if conv.Status == "waiting" {
					conv, e = m.Reception(ctx, conv.ID, Session{}, adminUser.ID, 0, conv.Version, "claim")
					if e != nil {
						return e
					}
				}
				if message.Text == "/claim" {
					return nil
				}
				saved, e := m.Send(ctx, conv.ID, Session{}, adminUser.ID, MessageInput{Version: conv.Version, RequestID: external, Content: message.Text, Visibility: "public", externalID: external})
				if e != nil {
					return e
				}
				return m.DB.WithContext(ctx).Model(&saved).Update("external_id", external).Error
			}
		}
	}
	mentioned := strings.Contains(strings.ToLower(message.Text), "@"+strings.ToLower(me.Username))
	if message.Chat.ID < 0 && !cfg.TelegramProactive && !mentioned && (message.ReplyToMessage == nil || message.ReplyToMessage.From == nil || message.ReplyToMessage.From.ID != me.ID) {
		return nil
	}
	identify := fmt.Sprintf("telegram:%d:%d:%d:%d", appID, message.Chat.ID, message.MessageThreadID, user.ID)
	var conv models.ChatConversation
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		conv = models.ChatConversation{Type: "support", AppID: appID, UserID: user.ID, EntryKey: "telegram", Identify: identify, Status: "ai", Version: 1, Round: 1, Context: Raw(telegramContext{ChatID: message.Chat.ID, ThreadID: message.MessageThreadID, MessageID: message.ID, Locale: locale, Channel: "telegram"})}
		if !cfg.AIEnabled {
			conv.Status = "waiting"
		}
		if e := tx.Where("identify = ?", identify).FirstOrCreate(&conv).Error; e != nil {
			return e
		}
		if e := tx.Model(&conv).Update("context", Raw(telegramContext{ChatID: message.Chat.ID, ThreadID: message.MessageThreadID, MessageID: message.ID, Locale: locale, Channel: "telegram"})).Error; e != nil {
			return e
		}
		traits := models.UserTraits{AppID: appID, UserID: user.ID, VerifiedAt: time.Now().UnixMilli(), Attributes: Raw(map[string]any{"telegram_id": message.From.ID, "telegram_username": message.From.Username})}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "app_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"attributes", "verified_at", "last_seen_at"})}).Create(&traits).Error
	})
	if err != nil {
		return err
	}
	session := Session{Record: authsession.Record{ID: "telegram", UserID: user.ID, AppID: appID, Verified: true}, User: user}
	if strings.HasPrefix(message.Text, "/human") {
		_, err = m.Reception(ctx, conv.ID, session, 0, 0, 0, "human")
		return err
	}
	saved, err := m.Send(ctx, conv.ID, session, 0, MessageInput{RequestID: external, Content: message.Text, Visibility: "public", externalID: external})
	if err != nil {
		return err
	}
	return m.DB.WithContext(ctx).Model(&saved).Update("external_id", external).Error
}
func (m *Manager) enqueueTelegram(ctx context.Context, appID, replyID uint, key string, delivery telegramDelivery) error {
	key = "telegram:send:" + strconv.FormatUint(uint64(appID), 10) + ":" + key
	return m.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&models.AIRun{AppID: appID, ReplyID: replyID, Kind: "telegram_send", RequestKey: &key, Input: Raw(delivery)}).Error
}

// QueueTelegram 将已提交的公开回复加入同一SQL执行表，投递失败可以重试。
func (m *Manager) QueueTelegram(ctx context.Context, conv models.ChatConversation, event models.ChatEvent) error {
	var channel telegramContext
	if json.Unmarshal(conv.Context, &channel) != nil || channel.Channel != "telegram" {
		return nil
	}
	var message models.ChatMessage
	if json.Unmarshal(event.Data, &message) != nil || message.ID == 0 || message.Status != "completed" || message.Visibility != "public" || strings.HasPrefix(message.SenderRealm, "support:") {
		return nil
	}
	if message.SenderRealm == "admin" && message.ExternalID != nil {
		return nil
	}
	if message.SenderRealm == "admin" {
		var stored models.ChatMessage
		if err := m.DB.WithContext(ctx).Select("id", "external_id").First(&stored, message.ID).Error; err != nil {
			return err
		}
		if stored.ExternalID != nil {
			return nil
		}
	}
	text := message.Content
	experts := []uint{}
	if message.Type == "event" {
		key := map[string]string{"human": "已申请人工客服", "claim": "客服已接待", "transfer": "已转交其他客服", "close": "会话已结束", "ai": "已恢复AI接待"}[text]
		if key == "" {
			return nil
		}
		text = i18n.Text(channel.Locale, key)
		if message.Content == "human" {
			var trigger models.ChatMessage
			if m.DB.WithContext(ctx).Where("conversation_id = ? AND type = ? AND sender_realm = ?", conv.ID, "text", Realm(conv.AppID)).Order("id DESC").First(&trigger).Error == nil {
				if entries, e := m.Search(ctx, conv.AppID, trigger.Content); e == nil {
					for _, entry := range entries {
						var ids []uint
						_ = json.Unmarshal(entry.Experts, &ids)
						experts = append(experts, ids...)
					}
				}
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return m.enqueueTelegram(ctx, conv.AppID, message.ID, strconv.FormatUint(uint64(message.ID), 10), telegramDelivery{telegramContext: channel, Text: text, Experts: slices.Compact(slices.Sorted(slices.Values(experts)))})
}
