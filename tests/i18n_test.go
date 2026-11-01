package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gorilla/websocket"
	"github.com/prismgo/framework/foundation"
)

func TestLanguages(t *testing.T) {
	r := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + r.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := migrations.Install(a.DB); err != nil {
		t.Fatal(err)
	}
	user := models.User{Username: "language", Super: true, Status: models.Enabled}
	if err := a.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := rand.Text()
	if err := a.DB.Create(&models.Session{ID: rand.Text(), UserID: user.ID, AccessHash: admin.TokenHash(token), RefreshHash: rand.Text(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(locale, method, path string, input any, authorized bool) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", locale)
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Error(err)
		}
		return response.Code, result
	}
	for _, tc := range []struct{ locale, message string }{
		{"en-US,en;q=0.8", "Your session has expired"}, {"zh-CN", "登录已失效"}, {"fr", "登录已失效"},
	} {
		status, result := call(tc.locale, "GET", "/auth/me", nil, false)
		if status != 401 || result["message"] != tc.message {
			t.Fatalf("%s: %d %v", tc.locale, status, result)
		}
	}
	var requests sync.WaitGroup
	for range 20 {
		for _, tc := range []struct{ locale, title string }{{"en", "Dashboard"}, {"zh", "仪表盘"}} {
			requests.Go(func() {
				status, result := call(tc.locale, "GET", "/menus", nil, true)
				if status != 200 {
					t.Error(result)
					return
				}
				rows := result["data"].([]any)
				if rows[0].(map[string]any)["title"] != tc.title {
					t.Errorf("并发语言串扰: %v", result)
				}
			})
		}
	}
	requests.Wait()
	status, result := call("en", "PUT", "/system/settings", map[string]any{"unknown": "value"}, true)
	if status != 422 || result["message"] != "Setting is not registered: unknown" {
		t.Fatal(result)
	}
	status, result = call("en", "POST", "/system/menus", map[string]any{"status": 1, "type": 4, "key": "custom.language", "parent_key": "missing", "title": "Language", "url": "/page", "mode": "iframe"}, true)
	if status != 422 || result["message"] != "Select a directory; buttons must belong to a menu" {
		t.Fatal(result)
	}
	status, result = call("en", "GET", "/system/settings", nil, true)
	if status != 200 {
		t.Fatal(result)
	}
	for _, raw := range result["data"].([]any) {
		row := raw.(map[string]any)
		if row["key"] == "site.title" && (row["title"] != "Site name" || row["group"] != "Site configuration") {
			t.Fatal(row)
		}
	}
	kernel := a.Runtime.(*foundation.Application).NewConsoleKernel()
	if err := kernel.Call(i18n.WithLocale(context.Background(), "en"), "dawn:start invalid"); err == nil || !strings.Contains(err.Error(), "Unknown service: invalid") {
		t.Fatalf("CLI language: %v", err)
	}
	server := httptest.NewServer(a.Realtime)
	defer server.Close()
	resume := ""
	for _, tc := range []struct{ locale, message string }{{"en", "Business message handler is not registered"}, {"zh", "未注册的业务消息"}} {
		status, result := call(tc.locale, "POST", "/realtime/ticket", map[string]any{"resume": resume}, true)
		if status != 200 {
			t.Fatal(result)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := conn.WriteJSON(realtime.Message{Type: "auth", Ticket: result["data"].(map[string]any)["ticket"].(string)}); err != nil {
			t.Fatal(err)
		}
		var message realtime.Message
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		var ready struct {
			Resume string `json:"resume"`
		}
		if err := json.Unmarshal(message.Data, &ready); err != nil {
			t.Fatal(err)
		}
		resume = ready.Resume
		if err := conn.WriteJSON(realtime.Message{Type: "unregistered"}); err != nil {
			t.Fatal(err)
		}
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		var text string
		if err := json.Unmarshal(message.Data, &text); err != nil {
			t.Fatal(err)
		}
		if message.Type != "error" || text != tc.message {
			t.Fatal(message.Type, text)
		}
		conn.Close()
	}
}
