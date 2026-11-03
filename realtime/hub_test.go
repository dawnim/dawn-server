package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

func TestOrigins(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	for _, tc := range []struct {
		name    string
		origins []string
		origin  string
		status  int
	}{
		{"all", []string{"*"}, "https://any.example", 101},
		{"allowed", []string{"https://admin.example"}, "https://admin.example", 101},
		{"denied", []string{"https://admin.example"}, "https://other.example", 403},
		{"same-origin", nil, "", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(client, "test:", "/")
			h.Origins = tc.origins
			defer h.Close()
			server := httptest.NewServer(h)
			defer server.Close()
			headers := http.Header{}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), headers)
			if conn != nil {
				defer conn.Close()
			}
			if response == nil || response.StatusCode != tc.status {
				t.Fatalf("握手结果: response=%v err=%v", response, err)
			}
		})
	}
}

// TestResumeAcrossInstances 验证跨实例补发、设备确认和一次性票据.
func TestResumeAcrossInstances(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	principal := Principal{Realm: "admin", Subject: "1", SessionID: "test-session"}
	newHub := func() *Hub {
		h := New(client, "test:", "/")
		h.Validate = func(ctx context.Context, p Principal) error {
			if p != principal {
				return fmt.Errorf("身份无效")
			}
			return nil
		}
		return h
	}
	first, second := newHub(), newHub()
	defer first.Close()
	defer second.Close()
	serverA, serverB := httptest.NewServer(first), httptest.NewServer(second)
	defer serverA.Close()
	defer serverB.Close()
	connect := func(h *Hub, url, resume string) (*websocket.Conn, string) {
		t.Helper()
		ticket, err := h.Ticket(context.Background(), principal, resume, "en")
		if err != nil {
			t.Fatal(err)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteJSON(Message{Type: "auth", Ticket: ticket}); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var ready Message
		if err := conn.ReadJSON(&ready); err != nil {
			t.Fatal(err)
		}
		var data struct {
			Resume string `json:"resume"`
		}
		if err := json.Unmarshal(ready.Data, &data); err != nil {
			t.Fatal(err)
		}
		if ready.Type != "ready" || data.Resume == "" {
			t.Fatal(ready)
		}
		return conn, data.Resume
	}
	conn, resume := connect(first, serverA.URL, "")
	id, err := second.Publish(context.Background(), principal, "test.event", map[string]string{"message": "one"})
	if err != nil {
		t.Fatal(err)
	}
	var message Message
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.ID != id {
		t.Fatal(message)
	}
	if err := conn.WriteJSON(Message{Type: "ack", ID: id, Channel: message.Channel}); err != nil {
		t.Fatal(err)
	}
	// ping的应答保证前一个ack已经由服务端处理.
	if err := conn.WriteJSON(Message{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	var pong Message
	if err := conn.ReadJSON(&pong); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	id2, err := first.Publish(context.Background(), principal, "test.event", map[string]string{"message": "two"})
	if err != nil {
		t.Fatal(err)
	}
	reconnected, _ := connect(second, serverB.URL, resume)
	defer reconnected.Close()
	if err := reconnected.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.ID != id2 {
		t.Fatalf("确认消息被重复补发: %#v", message)
	}
	if _, err := second.Ticket(context.Background(), Principal{Realm: "user", Subject: "1"}, resume, "en"); err == nil {
		t.Fatal("其他身份可以恢复管理员会话")
	}
}

func TestBusinessExtension(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	hub := New(client, "business:", "/")
	defer hub.Close()
	principal := Principal{Realm: "customer", Subject: "12", SessionID: "session"}
	hub.Validate = func(ctx context.Context, p Principal) error {
		if p != principal {
			return fmt.Errorf("unauthorized")
		}
		return nil
	}
	hub.Authorize = func(ctx context.Context, p Principal, channel string) error {
		if channel != "orders:12" {
			return fmt.Errorf("forbidden")
		}
		return nil
	}
	connected, disconnected := make(chan Principal, 1), make(chan Principal, 1)
	hub.Connected = func(ctx context.Context, p Principal) error { connected <- p; return nil }
	hub.Disconnected = func(ctx context.Context, p Principal) { disconnected <- p }
	if err := hub.Handle("app.echo", func(ctx context.Context, p Principal, data json.RawMessage) (any, error) {
		return map[string]any{"subject": p.Subject, "input": data}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if hub.Handle("app.echo", nil) == nil || hub.Handle("auth", nil) == nil || hub.Handle("ready", nil) == nil {
		t.Fatal("duplicate or reserved handler accepted")
	}
	hub.AuthorizeEvent = func(ctx context.Context, p Principal, event string) error {
		if event == "order.private" {
			return fmt.Errorf("forbidden")
		}
		return nil
	}
	server := httptest.NewServer(hub)
	defer server.Close()
	ticket, err := hub.Ticket(context.Background(), principal, "", "zh")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	conn.WriteJSON(Message{Type: "auth", Ticket: ticket})
	var message Message
	if err := conn.ReadJSON(&message); err != nil || message.Type != "ready" {
		t.Fatal(message, err)
	}
	if p := <-connected; p != principal {
		t.Fatal(p)
	}
	conn.WriteJSON(Message{Type: "app.echo", RequestID: "echo-1", Data: json.RawMessage(`{"value":1}`)})
	if err := conn.ReadJSON(&message); err != nil || message.Type != "result" || message.RequestID != "echo-1" || !strings.Contains(string(message.Data), `"subject":"12"`) {
		t.Fatal(message, err)
	}
	conn.WriteJSON(Message{Type: "subscribe", Channel: "orders:13", RequestID: "denied"})
	if err := conn.ReadJSON(&message); err != nil || message.Type != "error" {
		t.Fatal(message, err)
	}
	conn.WriteJSON(Message{Type: "subscribe", Channel: "orders:12", RequestID: "allowed"})
	if err := conn.ReadJSON(&message); err != nil || message.Type != "subscribed" {
		t.Fatal(message, err)
	}
	if _, err := hub.PublishChannel(context.Background(), "orders:12", "order.private", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.PublishChannel(context.Background(), "orders:12", "order.updated", map[string]int{"id": 12}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&message); err != nil || message.Type != "order.updated" {
		t.Fatal(message, err)
	}
	conn.Close()
	select {
	case p := <-disconnected:
		if p != principal {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect hook not called")
	}
}
