// Package realtime 提供共享身份入口和基于 Redis Stream 的跨实例恢复.
package realtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	randv2 "math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/constants"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

type Principal struct {
	Realm     string `json:"realm"`
	Subject   string `json:"subject"`
	SessionID string `json:"session_id"`
}

type Message struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	Ticket    string          `json:"ticket,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type Handler func(context.Context, Principal, json.RawMessage) (any, error)

type Hub struct {
	Redis     redis.UniversalClient
	Prefix    string
	Path      string
	Origins   []string
	Validate  func(context.Context, Principal) error
	Authorize func(context.Context, Principal, string) error
	Retention time.Duration
	Limit     int64
	mu        sync.RWMutex
	handlers  map[string]Handler
	ctx       context.Context
	cancel    context.CancelFunc
	drain     chan struct{}
	draining  bool
	active    sync.WaitGroup
}

type session struct {
	Principal Principal `json:"principal"`
	Locale    string    `json:"locale"`
	Resume    string    `json:"resume"`
	Channels  []string  `json:"channels"`
}

func New(client redis.UniversalClient, prefix, path string) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{Redis: client, Prefix: prefix, Path: path, Retention: 24 * time.Hour, Limit: 1000, handlers: map[string]Handler{}, ctx: ctx, cancel: cancel, drain: make(chan struct{})}
}

func (h *Hub) Close() { h.cancel() }

// Drain 停接新连接并分散通知旧连接重连，超时后回收剩余连接。
func (h *Hub) Drain(ctx context.Context) {
	h.mu.Lock()
	if !h.draining {
		h.draining = true
		close(h.drain)
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.active.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	h.Close()
	<-done
}

// Handle 注册普通业务函数, 无动态插件或自动扫描.
func (h *Hub) Handle(name string, handler Handler) error {
	if slices.Contains([]string{"auth", "ack", "subscribe", "unsubscribe", "ping"}, name) {
		return i18n.Errorf("消息名称被框架保留: %s", name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.handlers[name]; exists {
		return i18n.Errorf("消息处理器重复: %s", name)
	}
	h.handlers[name] = handler
	return nil
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (h *Hub) key(d constants.Definition, args ...any) string { return h.Prefix + d.Format(args...) }
func inbox(p Principal) string                                { return "inbox:" + hash(p.Realm+":"+p.Subject) }

// Ticket 只能由已完成HTTP身份验证的业务签发, 恢复凭证绑定原身份和登录会话.
func (h *Hub) Ticket(ctx context.Context, p Principal, resume, locale string) (string, error) {
	if h.Validate == nil {
		return "", i18n.Errorf("WS身份验证未配置")
	}
	if err := h.Validate(ctx, p); err != nil {
		return "", err
	}
	s := session{Principal: p, Resume: resume}
	if resume != "" {
		raw, err := h.Redis.Get(ctx, h.key(constants.TempWSSession, hash(resume))).Bytes()
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		if s.Principal != p {
			return "", i18n.Errorf("恢复凭证不属于当前登录会话")
		}
	} else {
		s.Resume = rand.Text()
		data, err := json.Marshal(s)
		if err != nil {
			return "", err
		}
		if err := h.Redis.Set(ctx, h.key(constants.TempWSSession, hash(s.Resume)), data, h.Retention).Err(); err != nil {
			return "", err
		}
	}
	s.Locale = locale
	ticket := rand.Text()
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return ticket, h.Redis.Set(ctx, h.key(constants.TempWSTicket, hash(ticket)), raw, constants.TempWSTicket.TTL).Err()
}

// Publish 把事件存入接收者收件流; 每个设备独立维护确认位置.
func (h *Hub) Publish(ctx context.Context, to Principal, event string, data any) (string, error) {
	return h.PublishChannel(ctx, inbox(to), event, data)
}

func (h *Hub) PublishChannel(ctx context.Context, channel, event string, data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	key := h.key(constants.TempWSStream, hash(channel))
	// 同一脚本写入并设置TTL, 避免实例退出后留下无期限消息流.
	value, err := h.Redis.Eval(ctx, `local id=redis.call('XADD',KEYS[1],'MAXLEN','=',ARGV[1],'*','type',ARGV[2],'data',ARGV[3]); redis.call('EXPIRE',KEYS[1],ARGV[4]); return id`, []string{key}, h.Limit, event, string(raw), int(h.Retention.Seconds())).Text()
	return value, err
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.Path {
		http.NotFound(w, r)
		return
	}
	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		http.Error(w, "Service restarting", http.StatusServiceUnavailable)
		return
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && (u.Host == r.Host || slices.Contains(h.Origins, "*") || slices.Contains(h.Origins, origin))
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetReadLimit(64 << 10)
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	stopConnection := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopConnection()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}
	var first Message
	if err := conn.ReadJSON(&first); err != nil || first.Type != "auth" {
		return
	}
	raw, err := h.Redis.GetDel(ctx, h.key(constants.TempWSTicket, hash(first.Ticket))).Bytes()
	if err != nil {
		return
	}
	var current session
	if err := json.Unmarshal(raw, &current); err != nil {
		return
	}
	if err := h.Validate(ctx, current.Principal); err != nil {
		return
	}
	ctx = i18n.WithLocale(ctx, current.Locale)
	stateKey := h.key(constants.TempWSSession, hash(current.Resume))
	leaseKey := h.key(constants.TempWSLease, hash(current.Resume))
	connectionID := rand.Text()
	if err := h.Redis.Set(ctx, leaseKey, connectionID, time.Minute).Err(); err != nil {
		return
	}
	defer func() {
		if err := h.Redis.Eval(context.WithoutCancel(ctx), `if redis.call('get',KEYS[1]) == ARGV[1] then return redis.call('del',KEYS[1]) end return 0`, []string{leaseKey}, connectionID).Err(); err != nil {
			slog.Error("释放WS连接租约失败", "error", err)
		}
	}()
	write := func(message Message) error {
		if message.Type == "error" {
			var key string
			if json.Unmarshal(message.Data, &key) == nil {
				message.Data, _ = json.Marshal(i18n.Text(current.Locale, key))
			}
		}
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		return conn.WriteJSON(message)
	}
	data, err := json.Marshal(map[string]any{"resume": current.Resume})
	if err != nil {
		return
	}
	if err := write(Message{Type: "ready", Data: data}); err != nil {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		return
	}
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(time.Minute)) })
	incoming := make(chan Message, 8)
	go func() {
		defer cancel()
		for {
			var message Message
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			if err := conn.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
				return
			}
			select {
			case incoming <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	channels := []string{inbox(current.Principal)}
	for _, channel := range current.Channels {
		if h.Authorize != nil && h.Authorize(ctx, current.Principal, channel) == nil {
			channels = append(channels, channel)
		}
	}
	cursors := map[string]string{}
	for _, channel := range channels {
		cursor, err := h.Redis.Get(ctx, h.key(constants.TempWSCursor, hash(current.Resume), hash(channel))).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return
		}
		if cursor == "" {
			cursor = "0-0"
		}
		cursors[channel] = cursor
	}
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	drain := h.drain
	var reconnect <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-drain:
			drain = nil
			reconnect = time.After(time.Duration(randv2.IntN(3000)) * time.Millisecond)
		case <-reconnect:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseServiceRestart, "Service restarting"), time.Now().Add(time.Second))
			return
		case <-heartbeat.C:
			if err := h.Validate(ctx, current.Principal); err != nil {
				return
			}
			valid, err := h.Redis.Eval(ctx, `if redis.call('get',KEYS[1]) == ARGV[1] then redis.call('expire',KEYS[1],60); redis.call('expire',KEYS[2],ARGV[2]); return 1 end return 0`, []string{leaseKey, stateKey}, connectionID, int(h.Retention.Seconds())).Int()
			if err != nil || valid != 1 {
				return
			}
			for _, channel := range slices.Clone(channels) {
				if channel == inbox(current.Principal) {
					continue
				}
				if h.Authorize == nil || h.Authorize(ctx, current.Principal, channel) != nil {
					channels = slices.DeleteFunc(channels, func(v string) bool { return v == channel })
					delete(cursors, channel)
				}
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		case message := <-incoming:
			owner, err := h.Redis.Get(ctx, leaseKey).Result()
			if err != nil || owner != connectionID {
				return
			}
			if err := h.Validate(ctx, current.Principal); err != nil {
				return
			}
			switch message.Type {
			case "ping":
				if err := write(Message{Type: "pong", RequestID: message.RequestID}); err != nil {
					return
				}
			case "ack":
				if !slices.Contains(channels, message.Channel) || !validID(message.ID) || compareID(message.ID, cursors[message.Channel]) > 0 {
					if err := write(Message{Type: "error", RequestID: message.RequestID, Data: json.RawMessage(`"确认位置无效"`)}); err != nil {
						return
					}
					continue
				}
				key := h.key(constants.TempWSCursor, hash(current.Resume), hash(message.Channel))
				old, err := h.Redis.Get(ctx, key).Result()
				if err != nil && !errors.Is(err, redis.Nil) {
					return
				}
				if old == "" || compareID(message.ID, old) > 0 {
					if err := h.Redis.Set(ctx, key, message.ID, h.Retention).Err(); err != nil {
						return
					}
				}
			case "subscribe", "unsubscribe":
				if message.Channel == "" || strings.HasPrefix(message.Channel, "inbox:") {
					continue
				}
				if message.Type == "subscribe" {
					if h.Authorize == nil || h.Authorize(ctx, current.Principal, message.Channel) != nil {
						if err := write(Message{Type: "error", RequestID: message.RequestID, Data: json.RawMessage(`"没有频道权限"`)}); err != nil {
							return
						}
						continue
					}
					if len(channels) >= 32 {
						continue
					}
					if !slices.Contains(channels, message.Channel) {
						channels = append(channels, message.Channel)
						cursor, err := h.Redis.Get(ctx, h.key(constants.TempWSCursor, hash(current.Resume), hash(message.Channel))).Result()
						if err != nil && !errors.Is(err, redis.Nil) {
							return
						}
						if cursor == "" {
							cursor = "0-0"
						}
						cursors[message.Channel] = cursor
					}
				} else {
					channels = slices.DeleteFunc(channels, func(v string) bool { return v == message.Channel })
					delete(cursors, message.Channel)
				}
				current.Channels = slices.DeleteFunc(slices.Clone(channels), func(v string) bool { return v == inbox(current.Principal) })
				raw, err := json.Marshal(current)
				if err != nil {
					return
				}
				if err := h.Redis.Set(ctx, stateKey, raw, h.Retention).Err(); err != nil {
					return
				}
				if err := write(Message{Type: "subscribed", Channel: message.Channel, RequestID: message.RequestID}); err != nil {
					return
				}
			default:
				h.mu.RLock()
				handler := h.handlers[message.Type]
				h.mu.RUnlock()
				if handler == nil {
					if err := write(Message{Type: "error", RequestID: message.RequestID, Data: json.RawMessage(`"未注册的业务消息"`)}); err != nil {
						return
					}
					continue
				}
				result, err := handler(ctx, current.Principal, message.Data)
				kind := "result"
				if err != nil {
					kind = "error"
					result = i18n.Message(err, current.Locale)
				}
				data, err := json.Marshal(result)
				if err != nil {
					return
				}
				if err := write(Message{Type: kind, RequestID: message.RequestID, Data: data}); err != nil {
					return
				}
			}
		case <-poll.C:
			owner, err := h.Redis.Get(ctx, leaseKey).Result()
			if err != nil || owner != connectionID {
				return
			}
			for _, channel := range channels {
				stream := h.key(constants.TempWSStream, hash(channel))
				cursor := cursors[channel]
				first, err := h.Redis.XRangeN(ctx, stream, "-", "+", 1).Result()
				if err != nil {
					return
				}
				if cursor != "0-0" && len(first) > 0 && compareID(cursor, first[0].ID) < 0 {
					if err := write(Message{Type: "resync_required", Channel: channel}); err != nil {
						return
					}
					cursor = "0-0"
					cursors[channel] = cursor
				}
				messages, err := h.Redis.XRangeN(ctx, stream, "("+cursor, "+", 100).Result()
				if err != nil {
					return
				}
				for _, message := range messages {
					if err := write(Message{Type: fmt.Sprint(message.Values["type"]), ID: message.ID, Channel: channel, Data: json.RawMessage(fmt.Sprint(message.Values["data"]))}); err != nil {
						return
					}
					cursors[channel] = message.ID
				}
			}
		}
	}
}

func validID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.ParseUint(p, 10, 64); err != nil {
			return false
		}
	}
	return true
}
func compareID(a, b string) int {
	aa, bb := strings.Split(a, "-"), strings.Split(b, "-")
	for i := range 2 {
		x, _ := strconv.ParseUint(aa[i], 10, 64)
		y, _ := strconv.ParseUint(bb[i], 10, 64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
