package tests

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

func offlineRuntime(t *testing.T) (*admin.App, models.SupportApp, customer.Session, models.ChatConversation, models.AdminUser) {
	t.Helper()
	a := customerRuntime(t)
	agent := models.AdminUser{Username: "offline-agent", Super: true}
	if err := a.DB.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	app := models.SupportApp{Name: "Offline", Config: customer.Raw(customer.Config{AIEnabled: true, AgentIDs: []uint{agent.ID}})}
	if err := a.Customer.CreateApp(context.Background(), &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := a.Customer.Open(context.Background(), app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	conv, err = a.Customer.Reception(context.Background(), conv.ID, visitor, 0, 0, 0, "human")
	if err != nil {
		t.Fatal(err)
	}
	conv, err = a.Customer.Reception(context.Background(), conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "claim")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Customer.ExpireOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, app, visitor, conv, agent
}

func TestCustomerOfflineTabsAndReconnect(t *testing.T) {
	a, app, visitor, conv, agent := offlineRuntime(t)
	ctx := context.Background()
	server := httptest.NewServer(a.Realtime)
	defer server.Close()
	principal := realtime.Principal{Realm: customer.Realm(app.ID), Subject: visitor.User.UniqueID, SessionID: visitor.ID}
	connect := func(resume string) (*websocket.Conn, string) {
		t.Helper()
		ticket, err := a.Realtime.Ticket(ctx, principal, resume, "zh")
		if err != nil {
			t.Fatal(err)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if err = conn.WriteJSON(map[string]any{"type": "auth", "ticket": ticket}); err != nil {
			t.Fatal(err)
		}
		var packet struct {
			Type string `json:"type"`
			Data struct {
				Resume string `json:"resume"`
			} `json:"data"`
		}
		if err = conn.ReadJSON(&packet); err != nil || packet.Type != "ready" {
			t.Fatal("handshake", packet.Type, err)
		}
		return conn, packet.Data.Resume
	}
	sweep := func() {
		t.Helper()
		if err := a.Customer.ExpireOffline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row := func() models.SupportSession {
		t.Helper()
		var s models.SupportSession
		if err := a.DB.First(&s, conv.SupportSession.ID).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	offline := func() {
		t.Helper()
		a.DB.Model(&models.SupportSession{}).Where("id = ?", conv.SupportSession.ID).Update("offline_since", time.Now().Add(-11*time.Minute).UnixMilli())
	}
	first, _ := connect("")
	second, _ := connect("")
	first.Close()
	sweep()
	if s := row(); s.OfflineSince != 0 || s.Status != "active" {
		t.Fatal("closing one tab ended reception", s)
	}
	second.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		seen, err := a.Realtime.Presence(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		if seen == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("last tab still online")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sweep()
	if s := row(); s.OfflineSince == 0 || s.Status != "active" {
		t.Fatal("short disconnect closed session", s)
	}
	offline()
	third, resume := connect("")
	sweep()
	if s := row(); s.OfflineSince != 0 || s.Status != "active" {
		t.Fatal("reconnect did not cancel timeout", s)
	}
	fourth, _ := connect(resume)
	third.Close()
	offline()
	sweep()
	if s := row(); s.Status != "active" || s.OfflineSince != 0 {
		t.Fatal("old connection cleanup removed resumed connection", s)
	}
	fourth.Close()
	deadline = time.Now().Add(2 * time.Second)
	for {
		seen, _ := a.Realtime.Presence(ctx, principal)
		if seen == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resumed connection still online")
		}
		time.Sleep(10 * time.Millisecond)
	}
	offline()
	sweep()
	closed := row()
	if closed.Status != "closed" || closed.CloseReason != "offline_timeout" || closed.Rating != nil || closed.ClosedAt == 0 {
		t.Fatal("timeout closure", closed)
	}
	var current models.ChatConversation
	a.DB.First(&current, conv.ID)
	if current.Status != "closed" || current.AssignedAdminID != 0 {
		t.Fatal("capacity not released", current)
	}
	var count int64
	a.DB.Model(&models.ChatMessage{}).Where("conversation_id = ? AND content = ?", conv.ID, "offline_closed").Count(&count)
	if count != 1 {
		t.Fatal("incorrect timeout event", count)
	}
	sweep()
	a.DB.Model(&models.ChatMessage{}).Where("conversation_id = ? AND content = ?", conv.ID, "offline_closed").Count(&count)
	if count != 1 {
		t.Fatal("duplicate closure", count)
	}
	if _, err := a.Customer.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "回来继续咨询"}); err != nil {
		t.Fatal(err)
	}
	a.DB.First(&current, conv.ID)
	if current.Round != conv.Round+1 || current.Status != "ai" || current.AssignedAdminID == agent.ID {
		t.Fatal("old reception revived", current)
	}
	if err := a.Customer.Rate(ctx, conv.ID, visitor, closed.ID, closed.Version, 3, "回来评价"); err != nil {
		t.Fatal("historical rating", err)
	}
}

func TestCustomerOfflinePendingQueueAndRecovery(t *testing.T) {
	for _, kind := range []string{"closing_pending", "waiting", "disabled", "telegram", "old_round", "redis_recovery", "service_recovery", "new_activity", "requeued_after_service"} {
		t.Run(kind, func(t *testing.T) {
			a, app, visitor, conv, agent := offlineRuntime(t)
			ctx := context.Background()
			m := a.Customer
			var err error
			if kind == "closing_pending" {
				conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "close")
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "waiting" || kind == "requeued_after_service" {
				a.DB.Model(&conv).Updates(map[string]any{"status": "waiting", "assigned_admin_id": 0})
				a.DB.Model(conv.SupportSession).Updates(map[string]any{"status": "waiting", "assigned_admin_id": 0})
			}
			if kind == "waiting" {
				a.DB.Model(conv.SupportSession).Update("started_at", 0)
			}
			if kind == "disabled" {
				cfg := customer.Config{OfflineTimeoutMinutes: new(int)}
				a.DB.Model(&app).Update("config", customer.Raw(cfg))
			}
			if kind == "telegram" {
				a.DB.Model(&conv).Update("context", customer.Raw(map[string]any{"channel": "telegram"}))
			}
			if kind == "old_round" {
				a.DB.Model(&conv).Update("round", conv.Round+1)
			}
			since := time.Now().Add(-11 * time.Minute).UnixMilli()
			if kind == "closing_pending" {
				since = time.Now().Add(-4 * time.Minute).UnixMilli()
			}
			a.DB.Model(conv.SupportSession).Updates(map[string]any{"offline_since": since, "closing_requested_at": since})
			if kind == "redis_recovery" {
				original := m.Redis
				broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, MaxRetries: -1})
				defer broken.Close()
				m.Redis = broken
				if err = m.ExpireOffline(ctx); err == nil {
					t.Fatal("Redis outage was ignored")
				}
				m.Redis = original
			}
			if kind == "new_activity" {
				if _, err = m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "我还在咨询"}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "service_recovery" {
				if err = m.Redis.Del(ctx, m.Key(constants.TempSupportPresenceCheck)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.ExpireOffline(ctx); err != nil {
				t.Fatal(err)
			}
			var row models.SupportSession
			a.DB.First(&row, conv.SupportSession.ID)
			switch kind {
			case "closing_pending", "requeued_after_service":
				if row.Status != "closed" || row.CloseReason != "offline_timeout" {
					t.Fatal(row)
				}
			case "waiting":
				if row.Status != "cancelled" || row.CloseReason != "queue_offline_timeout" {
					t.Fatal(row)
				}
				if err = m.Rate(ctx, conv.ID, visitor, row.ID, row.Version, 3, ""); err == nil {
					t.Fatal("cancelled queue accepted rating")
				}
			default:
				if row.Status == "closed" || row.Status == "cancelled" {
					t.Fatal("incorrect automatic closure", row)
				}
			}
			if kind == "redis_recovery" && row.OfflineSince != 0 {
				t.Fatal("recovery preserved expired timer", row)
			}
		})
	}
}
