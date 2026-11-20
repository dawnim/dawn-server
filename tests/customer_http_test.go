package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerHTTPInboxAndConfiguration(t *testing.T) {
	a := customerRuntime(t)
	agent := models.AdminUser{Username: "http-agent", Super: true}
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
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
		}
		var result map[string]any
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	call(401, "GET", "/admin/support-inbox", nil, "")
	call(200, "PUT", "/admin/support-presence", map[string]any{"online": true}, token)
	if call(200, "POST", "/admin/support-presence/heartbeats", map[string]any{}, token)["data"].(map[string]any)["online"] != true {
		t.Fatal("another tab changed available status")
	}
	call(200, "PUT", "/admin/support-presence", map[string]any{"online": false}, token)
	if call(200, "POST", "/admin/support-presence/heartbeats", map[string]any{}, token)["data"].(map[string]any)["online"] != false {
		t.Fatal("another tab changed unavailable status")
	}

	created := call(201, "POST", "/admin/support-apps", map[string]any{"name": "HTTP support", "config": customer.Config{AgentIDs: []uint{agent.ID}}, "secrets": customer.Secrets{IdentitySecret: "private-test-secret"}}, token)["data"].(map[string]any)
	appID := uint(created["id"].(float64))
	key := created["public_key"].(string)
	if created["secrets"] != nil {
		t.Fatal("secret leaked")
	}
	ctx := context.Background()
	session, conv, err := a.Customer.Open(ctx, key, "", customer.OpenInput{Attributes: map[string]any{"nick": "Customer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Model(&conv).Update("status", "ai").Error; err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(session)
	var public map[string]any
	_ = json.Unmarshal(serialized, &public)
	if public["user_id"] != nil || public["id"] != nil || public["user"].(map[string]any)["id"] != nil {
		t.Fatal("public session leaked internal user IDs")
	}
	call(200, "PATCH", "/api/support/"+key+"/context", map[string]any{"attributes": map[string]any{"plan": "pro", "user_id": 999}, "observed": map[string]any{"ip": "8.8.8.8"}}, session.Token)
	var traits models.UserTraits
	if err = a.DB.Where("app_id = ? AND user_id = ?", appID, session.UserID).First(&traits).Error; err != nil {
		t.Fatal(err)
	}
	var attributes map[string]any
	_ = json.Unmarshal(traits.Attributes, &attributes)
	if attributes["plan"] != "pro" || traits.VerifiedAt != 0 {
		t.Fatal("custom attributes changed identity verification", attributes)
	}

	if _, err = a.Customer.Send(ctx, conv.ID, session, 0, customer.MessageInput{Content: "question", RequestID: rand.Text()}); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.First(&conv, conv.ID).Error; err != nil {
		t.Fatal(err)
	}
	preview := call(200, "GET", "/admin/support-inbox", nil, token)
	if len(preview["data"].([]any)) != 1 || preview["data"].([]any)[0].(map[string]any)["content"] != "question" {
		t.Fatal("offline inbox hid unassigned messages", preview)
	}
	if preview["meta"].(map[string]any)["unread_total"].(float64) != 0 {
		t.Fatal("AI conversation triggered the agent notification", preview)
	}
	if preview["data"].([]any)[0].(map[string]any)["unread"].(float64) != 0 {
		t.Fatal("AI conversation displayed a list unread count", preview)
	}
	call(201, "POST", fmt.Sprintf("/api/support/%s/conversations/%d/human-requests", key, conv.ID), map[string]any{}, session.Token)
	requested := call(200, "GET", "/admin/support-inbox", nil, token)
	if requested["meta"].(map[string]any)["unread_total"].(float64) != 1 {
		t.Fatal("human request did not trigger the agent notification", requested)
	}
	if requested["data"].([]any)[0].(map[string]any)["unread"].(float64) != 1 {
		t.Fatal("human request did not display the list unread count", requested)
	}
	call(200, "GET", fmt.Sprintf("/admin/support-conversations/%d/messages", conv.ID), nil, token)
	call(200, "PUT", fmt.Sprintf("/admin/support-conversations/%d/read-position", conv.ID), map[string]any{"message_id": conv.LastMessageID}, token)
	read := call(200, "GET", "/admin/support-inbox", nil, token)
	if read["meta"].(map[string]any)["unread_total"].(float64) != 0 {
		t.Fatal("offline viewing did not clear unread", read)
	}
	if err = a.DB.First(&conv, conv.ID).Error; err != nil || conv.AssignedAdminID != 0 {
		t.Fatal("viewing a conversation claimed it", conv, err)
	}
	if _, err = a.Customer.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "claim"); err != nil {
		t.Fatal(err)
	}
	draftPath := fmt.Sprintf("/admin/support-conversations/%d/drafts", conv.ID)
	draft1 := call(201, "POST", draftPath, map[string]any{}, token)["data"].(map[string]any)
	draft2 := call(201, "POST", draftPath, map[string]any{}, token)["data"].(map[string]any)
	if draft1["id"] != draft2["id"] {
		t.Fatal("duplicate draft request created another run")
	}
	inbox := call(200, "GET", "/admin/support-inbox?mine=1&status=human", nil, token)
	if len(inbox["data"].([]any)) != 1 || inbox["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatal(inbox)
	}
	if stats := inbox["meta"].(map[string]any)["statistics"].(map[string]any); stats["customers"].(float64) != 1 || stats["replies"].(float64) != 0 {
		t.Fatal("incorrect reception statistics", stats)
	}
	for _, sample := range []struct {
		visibility, status, subject string
		createdAt                   int64
	}{
		{"public", "completed", strconv.FormatUint(uint64(agent.ID), 10), time.Now().UnixMilli()},
		{"internal", "completed", strconv.FormatUint(uint64(agent.ID), 10), time.Now().UnixMilli()},
		{"public", "failed", strconv.FormatUint(uint64(agent.ID), 10), time.Now().UnixMilli()},
		{"public", "completed", "999", time.Now().UnixMilli()},
		{"public", "completed", strconv.FormatUint(uint64(agent.ID), 10), time.Now().AddDate(0, 0, -1).UnixMilli()},
	} {
		row := models.ChatMessage{ConversationID: conv.ID, SenderRealm: "admin", SenderSubject: sample.subject, RequestID: rand.Text(), Content: "statistics", Type: "text", Visibility: sample.visibility, Status: sample.status, CreatedAt: sample.createdAt}
		if err = a.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	stats := call(200, "GET", "/admin/support-inbox?status=closed", nil, token)["meta"].(map[string]any)["statistics"].(map[string]any)
	if stats["replies"].(float64) != 1 || stats["customers"].(float64) != 1 {
		t.Fatal("statistics included another agent, notes, failures or previous days", stats)
	}
	direct := call(200, "GET", "/admin/chat-conversations", nil, token)
	if direct["meta"].(map[string]any)["unread_total"].(float64) != 0 {
		t.Fatal("support unread counted as direct", direct)
	}
	call(200, "PATCH", fmt.Sprintf("/admin/support-apps/%d", appID), map[string]any{"name": "Updated", "revision": created["revision"]}, token)
	_, _, secret, err := a.Customer.App(ctx, appID)
	if err != nil || secret.IdentitySecret != "private-test-secret" {
		t.Fatal("PATCH lost encrypted secret", err)
	}
	call(200, "GET", "/api/support/"+key+"/configuration", nil, "")
	ticket := call(201, "POST", "/api/support/"+key+"/realtime-tickets", map[string]any{}, session.Token)["data"].(map[string]any)
	if ticket["ws_url"] != "ws://example.com:8851/" {
		t.Fatal("direct visitor page did not receive the standalone WS address", ticket["ws_url"])
	}
	proxyRequest := httptest.NewRequest("POST", "/api/support/"+key+"/realtime-tickets", bytes.NewReader([]byte(`{}`)))
	proxyRequest.Header.Set("Content-Type", "application/json")
	proxyRequest.Header.Set("Authorization", "Bearer "+session.Token)
	proxyRequest.Header.Set("X-Forwarded-Proto", "https")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != 201 {
		t.Fatalf("proxied realtime ticket: %d %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	var proxyResult map[string]any
	if err := json.Unmarshal(proxyResponse.Body.Bytes(), &proxyResult); err != nil {
		t.Fatal(err)
	}
	if proxyResult["data"].(map[string]any)["ws_url"] != "wss://example.com/ws" {
		t.Fatal("proxied visitor page did not receive the same-origin secure WS address", proxyResult["data"])
	}

	foreign, _, err := a.Customer.Open(ctx, key, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	call(403, "GET", fmt.Sprintf("/api/support/%s/conversations/%d/messages", key, conv.ID), nil, foreign.Token)
	role := models.Role{Name: "Knowledge reader", Code: "knowledge-reader", DataScope: models.ScopeAll}
	if err = a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: "dawn.knowledge.list"}).Error; err != nil {
		t.Fatal(err)
	}
	reader := models.AdminUser{Username: "knowledge-reader", Roles: []models.Role{role}}
	if err = a.DB.Create(&reader).Error; err != nil {
		t.Fatal(err)
	}
	_, readerToken := testSession(t, a, reader.ID)
	call(403, "GET", "/admin/support-apps", nil, readerToken)
	options := call(200, "GET", "/admin/knowledge-apps", nil, readerToken)["data"].([]any)
	if len(options) != 1 || len(options[0].(map[string]any)) != 2 {
		t.Fatal("knowledge options leaked configuration", options)
	}

	base := fmt.Sprintf("/api/support/%s/conversations/%d", key, conv.ID)
	current := call(200, "GET", base, nil, session.Token)["data"].(map[string]any)
	closed := call(200, "PUT", fmt.Sprintf("/admin/support-conversations/%d/reception", conv.ID), map[string]any{"action": "close", "version": current["version"]}, token)["data"].(map[string]any)
	reception := closed["support_session"].(map[string]any)
	if closed["status"] != "human" || reception["status"] != "closing_pending" {
		t.Fatal("HTTP premature close", closed)
	}
	decision := map[string]any{"session_id": reception["id"], "version": reception["version"], "approved": true}
	call(403, "PUT", base+"/closing-request", decision, foreign.Token)
	call(422, "POST", base+"/ratings", map[string]any{"session_id": reception["id"], "rating": 0}, session.Token)
	call(200, "PUT", base+"/closing-request", decision, session.Token)
	call(200, "PUT", base+"/closing-request", decision, session.Token)
	call(201, "POST", base+"/messages", map[string]any{"request_id": rand.Text(), "content": "新的问题"}, session.Token)
	call(201, "POST", base+"/ratings", map[string]any{"session_id": reception["id"], "rating": 1, "comment": "差评！！！！"}, session.Token)
	call(201, "POST", base+"/ratings", map[string]any{"session_id": reception["id"], "rating": 1, "comment": "差评！！！！"}, session.Token)
	call(403, "GET", fmt.Sprintf("%s/support-sessions/%v", base, reception["id"]), nil, foreign.Token)

}
