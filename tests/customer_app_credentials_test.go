package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerApplicationCredentials(t *testing.T) {
	a := customerRuntime(t)
	agent := models.AdminUser{Username: "config-agent", Super: true}
	if err := a.DB.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	_, token := testSession(t, a, agent.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(want int, method, path string, input any, access string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
		}
		if strings.HasSuffix(path, "/secrets") && res.Code == 200 && res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secrets were cacheable")
		}
		var result map[string]any
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("x-api-key") != "" {
			key = r.Header.Get("x-api-key")
		}
		if key != "valid-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"Invalid API key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/responses":
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
		case "/v1/chat/completions":
			fmt.Fprint(w, "data: {\"id\":\"chat_test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/v1/messages":
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			t.Errorf("unexpected model endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer model.Close()
	for _, protocol := range []string{"responses", "chat_completions", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			config := customer.Config{Model: "test-model", ModelProtocol: protocol, ReasoningEffort: "vendor-custom", BaseURL: model.URL}
			input := map[string]any{"name": protocol, "config": config, "secrets": customer.Secrets{APIKey: "invalid-key", IdentitySecret: "signing", TelegramToken: "123:test"}}
			call(422, "POST", "/support-apps", input, token)
			var count int64
			if err := a.DB.Model(&models.SupportApp{}).Where("name = ?", protocol).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("invalid creation was persisted", count, err)
			}
			input["secrets"] = customer.Secrets{APIKey: "valid-key", IdentitySecret: "signing", TelegramToken: "123:test"}
			// Each bot belongs to one app; only the first app needs Telegram credentials.
			if protocol != "responses" {
				input["secrets"] = customer.Secrets{APIKey: "valid-key", IdentitySecret: "signing"}
			}
			created := call(201, "POST", "/support-apps", input, token)["data"].(map[string]any)
			id := uint(created["id"].(float64))
			path := fmt.Sprintf("/support-apps/%d", id)
			if created["secrets"] != nil {
				t.Fatal("creation exposed secrets")
			}
			revealed := call(200, "GET", path+"/secrets", nil, token)["data"].(map[string]any)
			if len(revealed) != 2 || revealed["identity_secret"] != "signing" || revealed["api_key"] != nil {
				t.Fatal("incorrect reveal fields", revealed)
			}
			call(401, "GET", path+"/secrets", nil, "")
			call(422, "PATCH", path, map[string]any{"revision": 1, "secrets": map[string]any{"api_key": nil}}, token)
			call(422, "PATCH", path, map[string]any{"revision": 1, "secrets": map[string]any{"unknown": "value"}}, token)
			call(422, "PATCH", path, map[string]any{"name": "must-not-save", "revision": created["revision"], "secrets": map[string]any{"api_key": "invalid-key"}}, token)
			app, _, secrets, err := a.Customer.App(context.Background(), id)
			if err != nil || app.Name != protocol || app.Revision != 1 || secrets.APIKey != "valid-key" {
				t.Fatal("rejected key changed configuration", app.Name, app.Revision, err)
			}
			config.IndexProfile = "active"
			config.ActiveIndex = &customer.IndexSpec{Profile: "active", Driver: "portable", Dimensions: 384}
			if err := a.DB.Model(&app).Update("config", customer.Raw(config)).Error; err != nil {
				t.Fatal(err)
			}
			call(200, "PATCH", path, map[string]any{"revision": 1, "name": "updated", "config": customer.Config{Model: "new-model", IndexProfile: "client"}}, token)
			_, savedConfig, _, err := a.Customer.App(context.Background(), id)
			if err != nil || savedConfig.Model != "new-model" || savedConfig.IndexProfile != "active" || savedConfig.ActiveIndex == nil || *savedConfig.ActiveIndex != *config.ActiveIndex {
				t.Fatal("configuration update changed the active index", savedConfig, err)
			}
			call(409, "PATCH", path, map[string]any{"revision": 1, "secrets": map[string]any{"api_key": "valid-key"}}, token)
			call(204, "DELETE", path, nil, token)
			call(404, "GET", path, nil, token)
		})
	}
	role := models.Role{Name: "Own app editor", Code: "own_app_editor", DataScope: models.ScopeSelf}
	if err := a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dawn.support_apps.update", "dawn.support_apps.delete"} {
		if err := a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	editor := models.AdminUser{Username: "own-editor", Roles: []models.Role{role}}
	viewer := models.AdminUser{Username: "no-config-access"}
	if err := a.DB.Create(&editor).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&viewer).Error; err != nil {
		t.Fatal(err)
	}
	_, editorToken := testSession(t, a, editor.ID)
	_, viewerToken := testSession(t, a, viewer.ID)
	own := models.SupportApp{Name: "Own", CreatedBy: editor.ID}
	foreign := models.SupportApp{Name: "Foreign", CreatedBy: agent.ID}
	for _, app := range []*models.SupportApp{&own, &foreign} {
		if err := a.Customer.CreateApp(context.Background(), app, customer.Secrets{}); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/support-apps/%d", foreign.ID)
	call(403, "GET", path+"/secrets", nil, viewerToken)
	call(404, "GET", path+"/secrets", nil, editorToken)
	call(404, "GET", path+"/telegram-groups", nil, editorToken)
	call(404, "DELETE", fmt.Sprintf("/support-apps?ids=%d,%d", own.ID, foreign.ID), nil, editorToken)
	var remaining int64
	a.DB.Model(&models.SupportApp{}).Where("id IN ?", []uint{own.ID, foreign.ID}).Count(&remaining)
	if remaining != 2 {
		t.Fatal("mixed-scope deletion removed data", remaining)
	}
}
