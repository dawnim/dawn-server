package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestChat(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	users := []models.User{
		{Username: "alice", Status: 1, Super: true},
		{Username: "bob", Status: 1, Super: true},
		{Username: "outsider", Status: 1, Super: true},
		{Username: "denied", Status: 1},
	}
	tokens := make([]string, len(users))
	sessions := make([]string, len(users))
	for i := range users {
		if err := a.DB.Create(&users[i]).Error; err != nil {
			t.Fatal(err)
		}
		tokens[i], sessions[i] = rand.Text(), rand.Text()
		if err := a.DB.Create(&models.Session{ID: sessions[i], UserID: users[i].ID, AccessHash: admin.TokenHash(tokens[i]), RefreshHash: rand.Text(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(want int, actor int, method, path string, input any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(raw))
		if actor >= 0 {
			req.Header.Set("Authorization", "Bearer "+tokens[actor])
		}
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	call(401, -1, "GET", "/chat-conversations", nil)
	call(200, 3, "GET", "/chat-conversations", nil)
	contacts := call(200, 3, "GET", "/chat-contacts", nil)
	if len(contacts["data"].([]any)) != 3 {
		t.Fatal("contacts unavailable to ordinary admin", contacts)
	}
	if _, exists := contacts["data"].([]any)[0].(map[string]any)["email"]; exists {
		t.Fatal("contact exposed private fields")
	}
	ordinary := call(201, 3, "POST", "/chat-conversations", map[string]any{"user_id": users[0].ID})["data"].(map[string]any)
	call(422, 0, "POST", "/chat-conversations", map[string]any{"user_id": users[0].ID})
	data := call(201, 0, "POST", "/chat-conversations", map[string]any{"user_id": users[1].ID})["data"].(map[string]any)
	id := uint(data["id"].(float64))
	path := fmt.Sprintf("/chat-conversations/%d", id)
	repeated := call(200, 1, "POST", "/chat-conversations", map[string]any{"user_id": users[0].ID})["data"].(map[string]any)
	if repeated["id"] != data["id"] {
		t.Fatal("duplicate direct conversation")
	}
	call(404, 2, "GET", path+"/messages", nil)
	call(404, 2, "POST", path+"/messages", map[string]any{"request_id": "00000000-0000-4000-8000-000000000001", "content": "secret"})
	call(422, 0, "POST", path+"/messages", map[string]any{"request_id": "00000000-0000-4000-8000-000000000001", "content": "   "})
	input := map[string]any{"request_id": "00000000-0000-4000-8000-000000000001", "content": "hello"}
	msg := call(201, 0, "POST", path+"/messages", input)["data"].(map[string]any)
	call(200, 1, "GET", fmt.Sprintf("%s/messages/%.0f", path, msg["id"]), nil)
	call(404, 2, "GET", fmt.Sprintf("%s/messages/%.0f", path, msg["id"]), nil)
	retry := call(201, 0, "POST", path+"/messages", input)["data"].(map[string]any)
	if msg["id"] != retry["id"] {
		t.Fatal("idempotency failed")
	}
	call(409, 0, "POST", path+"/messages", map[string]any{"request_id": input["request_id"], "content": "different"})
	second := call(201, 0, "POST", path+"/messages", map[string]any{"request_id": "00000000-0000-4000-8000-000000000002", "content": "second"})["data"].(map[string]any)
	list := call(200, 1, "GET", "/chat-conversations", nil)
	if list["meta"].(map[string]any)["unread_total"] != float64(2) {
		t.Fatal("unread count", list)
	}
	history := call(200, 1, "GET", path+"/messages?page_size=1", nil)
	if !history["meta"].(map[string]any)["has_more"].(bool) {
		t.Fatal("missing history cursor")
	}
	earlier := call(200, 1, "GET", fmt.Sprintf("%s/messages?before_id=%.0f", path, second["id"]), nil)
	if len(earlier["data"].([]any)) != 1 {
		t.Fatal("cursor history", earlier)
	}
	call(404, 1, "PUT", path+"/read-position", map[string]any{"message_id": 99999})
	call(200, 1, "PUT", path+"/read-position", map[string]any{"message_id": second["id"]})
	call(200, 1, "PUT", path+"/read-position", map[string]any{"message_id": msg["id"]})
	var member models.ChatMember
	a.DB.Where("conversation_id = ? AND subject = ?", id, fmt.Sprint(users[1].ID)).First(&member)
	if member.LastReadMessageID != uint(second["id"].(float64)) {
		t.Fatal("read position moved backwards")
	}
	list = call(200, 1, "GET", "/chat-conversations", nil)
	if list["meta"].(map[string]any)["unread_total"] != float64(0) {
		t.Fatal("read count", list)
	}
	// Redis failure keeps the SQL outbox pending and does not duplicate saved messages.
	healthy := a.Realtime.Redis
	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Millisecond})
	a.Realtime.Redis = broken
	if err := a.PublishChatEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	broken.Close()
	a.Realtime.Redis = healthy
	var pending int64
	a.DB.Model(&models.ChatEvent{}).Where("published_at = 0 AND attempts > 0").Count(&pending)
	if pending != 3 {
		t.Fatal("events lost on failed publication", pending)
	}
	a.DB.Model(&models.ChatEvent{}).Where("published_at = 0").Update("available_at", 0)
	server := httptest.NewServer(a.Realtime)
	defer server.Close()
	ticket, err := a.Realtime.Ticket(context.Background(), realtime.Principal{Realm: "admin", Subject: fmt.Sprint(users[1].ID), SessionID: sessions[1]}, "", "en")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	conn.WriteJSON(realtime.Message{Type: "auth", Ticket: ticket})
	var event realtime.Message
	if err := conn.ReadJSON(&event); err != nil || event.Type != "ready" {
		t.Fatal(event, err)
	}
	if err := a.PublishChatEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&event); err != nil || event.Type != "chat.message.created" {
		t.Fatal(event, err)
	}
	var envelope struct {
		EventID uint               `json:"event_id"`
		Data    models.ChatMessage `json:"data"`
	}
	if err := json.Unmarshal(event.Data, &envelope); err != nil || envelope.EventID == 0 || envelope.Data.Content != "hello" {
		t.Fatal(envelope, err)
	}
	a.DB.Model(&models.ChatEvent{}).Where("published_at = 0").Count(&pending)
	if pending != 0 {
		t.Fatal("pending events", pending)
	}
	a.DB.Model(&users[1]).Update("status", 2)
	call(403, 0, "POST", path+"/messages", map[string]any{"request_id": "00000000-0000-4000-8000-000000000003", "content": "disabled"})
	call(201, 3, "POST", fmt.Sprintf("/chat-conversations/%.0f/messages", ordinary["id"]), map[string]any{"request_id": "00000000-0000-4000-8000-000000000004", "content": "ordinary admin can send"})
	var logs []models.Log
	a.DB.Where("path LIKE ?", "%chat-conversations%").Find(&logs)
	for _, log := range logs {
		if log.RequestData != "" || log.ResponseData != "" {
			t.Fatal("chat content leaked into audit", log.ID)
		}
	}
}

func TestChatDispatcherSleepsUntilWake(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int64
	a.DB.Callback().Query().After("gorm:query").Register("test:chat_queries", func(db *gorm.DB) {
		if db.Statement.Table == "chat_events" {
			queries.Add(1)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.DispatchChat(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("dispatcher did not stop promptly")
		}
	}()
	deadline := time.Now().Add(time.Second)
	for queries.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if queries.Load() != 1 {
		t.Fatal("startup reconciliation missing", queries.Load())
	}
	time.Sleep(1200 * time.Millisecond)
	if queries.Load() != 1 {
		t.Fatal("idle dispatcher polled SQL", queries.Load())
	}
	if err := a.DB.Create(&models.ChatMember{ConversationID: 1, Realm: "admin", Subject: "1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&models.ChatEvent{ConversationID: 1, Type: "chat.message.created", Data: json.RawMessage(`{"id":1}`)}).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	a.WakeChat(ctx)
	for queries.Load() == 1 && time.Since(started) < time.Second {
		time.Sleep(5 * time.Millisecond)
	}
	if queries.Load() != 2 {
		t.Fatal("notification did not wake SQL dispatcher", queries.Load())
	}
	// Publication may still be finishing; check the inbox instead of generating SQL from the test.
	for time.Since(started) < time.Second {
		for _, key := range store.Keys() {
			if strings.Contains(key, "ws_stream") && store.Exists(key) {
				t.Logf("wake-to-publish: %s; SQL queries: startup=1, idle=0, wake=1", time.Since(started))
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("message was not published after wakeup")
}
