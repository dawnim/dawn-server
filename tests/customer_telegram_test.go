package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerTelegramLifecycle(t *testing.T) {
	a := customerRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admin := models.AdminUser{Username: "telegram-agent", Super: true}
	if err := a.DB.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	code, err := a.Customer.CreateTelegramBinding(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	delivered := map[string]bool{}
	sent := map[string]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := int64(900001)
		if strings.Contains(r.URL.Path, "900002:") {
			id++
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprintf(w, `{"ok":true,"result":{"id":%d,"is_bot":true,"first_name":"Support","username":"support_bot"}}`, id)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			mu.Lock()
			first := !delivered[r.URL.Path]
			delivered[r.URL.Path] = true
			mu.Unlock()
			if first {
				// 两个机器人共用一个Telegram用户，身份全局唯一，会话仍按应用隔离。
				fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"from":{"id":77,"is_bot":false,"first_name":"Agent","username":"agent_name","language_code":"zh"},"chat":{"id":77,"type":"private"},"text":%q}},{"update_id":2,"message":{"message_id":2,"from":{"id":77,"is_bot":false,"first_name":"Agent","username":"agent_name","language_code":"zh"},"chat":{"id":-100,"type":"supergroup"},"text":"@support_bot 请帮忙"}},{"update_id":3,"my_chat_member":{"chat":{"id":-200,"type":"supergroup","title":"New group"},"from":{"id":77,"is_bot":false},"date":1,"old_chat_member":{"status":"left","user":{"id":900001,"is_bot":true}},"new_chat_member":{"status":"member","user":{"id":900001,"is_bot":true}}}}]}`, "/bind "+code)
			} else {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(30 * time.Millisecond):
				}
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			mu.Lock()
			sent[r.URL.Path]++
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":100,"chat":{"id":77,"type":"private"},"date":1,"text":"ok"}}`)
		default:
			t.Errorf("unexpected Telegram method %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	a.Customer.TelegramURL = api.URL
	apps := make([]models.SupportApp, 2)
	for i := range apps {
		apps[i] = models.SupportApp{Name: fmt.Sprintf("Bot %d", i), Config: customer.Raw(customer.Config{TelegramEnabled: true, TelegramGroups: []int64{-100}})}
		if err := a.Customer.CreateApp(ctx, &apps[i], customer.Secrets{TelegramToken: fmt.Sprintf("%d:test", 900001+i)}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); a.Customer.RunTelegram(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Telegram goroutines did not exit")
		}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var count int64
		if err := a.DB.Model(&models.ChatConversation{}).Where("entry_key = ? AND status = ?", "telegram", "waiting").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		sendCount := len(sent)
		mu.Unlock()
		if count == 2 && sendCount == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Telegram update/send did not complete: conversations=%d deliveries=%d", count, sendCount)
		}
		time.Sleep(30 * time.Millisecond)
	}
	for i, app := range apps {
		value, err := a.Customer.Redis.HGet(ctx, a.Customer.Key(constants.TempTelegramGroups, app.ID, 900001+i), "-200").Result()
		if err != nil || !strings.Contains(value, "New group") {
			t.Fatal("undiscovered unapproved group", value, err)
		}
	}
	_, adminToken := testSession(t, a, admin.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/admin/support-apps/%d/telegram-groups", apps[0].ID), nil)
	request.Header.Set("Authorization", "Bearer "+adminToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "New group") || !strings.Contains(response.Body.String(), "-200") {
		t.Fatal("group candidates unavailable", response.Code, response.Body.String())
	}
	binding, err := a.Customer.TelegramBinding(ctx, admin.ID, code)
	if err != nil || binding.TelegramID != 77 || binding.Username != "agent_name" {
		t.Fatalf("binding: %+v %v", binding, err)
	}
	if err = a.Customer.ConfirmTelegramBinding(ctx, admin.ID, code); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.First(&admin, admin.ID).Error; err != nil || admin.UserID == nil || *admin.UserID != binding.UserID {
		t.Fatal("admin binding", err)
	}
	var user models.User
	if err = a.DB.First(&user, binding.UserID).Error; err != nil || user.TelegramID == nil || *user.TelegramID != 77 {
		t.Fatal("member identity", err)
	}
	var runs []models.AIRun
	if err = a.DB.Where("kind = ?", "telegram_update").Find(&runs).Error; err != nil || len(runs) != 6 {
		t.Fatalf("durable updates=%d %v", len(runs), err)
	}
	for _, run := range runs {
		if !json.Valid(run.Input) {
			t.Fatal("invalid durable update")
		}
	}
	if err = a.DB.Model(&user).Update("status", models.Disabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.Customer.TelegramUser(ctx, customer.TelegramProfile{ID: 77, Username: "changed"}); err == nil {
		t.Fatal("disabled Telegram identity accepted")
	}
}
