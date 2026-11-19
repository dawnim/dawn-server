package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerKnowledgeCRUD(t *testing.T) {
	a := customerRuntime(t)
	owner := models.AdminUser{Username: "knowledge-owner", Super: true}
	if err := a.DB.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	_, token := testSession(t, a, owner.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(want int, method, path string, input any, access string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body.String())
		}
		if want == 201 && res.Header().Get("Location") == "" {
			t.Fatal("missing Location")
		}
		var result map[string]any
		if res.Body.Len() > 0 && json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatal(res.Body.String())
		}
		return result
	}
	app := models.SupportApp{Name: "Knowledge", CreatedBy: owner.ID}
	other := models.SupportApp{Name: "Other", CreatedBy: owner.ID}
	for _, row := range []*models.SupportApp{&app, &other} {
		if err = a.Customer.CreateApp(context.Background(), row, customer.Secrets{}); err != nil {
			t.Fatal(err)
		}
	}
	appPath := fmt.Sprintf("/support-apps/%d", app.ID)
	call(422, "PATCH", appPath, map[string]any{"name": nil, "revision": 1}, token)
	call(200, "PATCH", appPath, map[string]any{"name": "这是一个超过三十三个汉字的客服应用名称用来确认按字符而非字节计算名称长度不会误伤正常的中文应用名称", "revision": 1}, token)
	source := call(201, "POST", "/knowledge-sources", map[string]any{"app_id": app.ID, "name": "Guide", "kind": "text", "content": "First"}, token)["data"].(map[string]any)
	sourceID := uint(source["id"].(float64))
	sourcePath := fmt.Sprintf("/knowledge-sources/%d", sourceID)
	call(200, "PATCH", sourcePath, map[string]any{"content": "Updated"}, token)
	call(422, "PATCH", sourcePath, map[string]any{"app_id": other.ID}, token)
	call(422, "PUT", sourcePath, map[string]any{"name": "Missing content"}, token)
	telegramID := int64(123456)
	expert := models.User{Nickname: "Bound", TelegramID: &telegramID, Status: models.Enabled}
	if err = a.DB.Create(&expert).Error; err != nil {
		t.Fatal(err)
	}
	call(200, "GET", fmt.Sprintf("/knowledge-experts?ids=%d", expert.ID), nil, token)
	first := map[string]any{"app_id": app.ID, "source_id": sourceID, "question": "Install", "answer": "## Setup\n\nUse the installer", "experts": []uint{expert.ID}}
	second := map[string]any{"app_id": app.ID, "question": "Next", "answer": ""}
	batch := map[string]any{"entries": []any{first, second}}
	call(422, "POST", "/knowledge-entry-batches", batch, token)
	var count int64
	if err = a.DB.Model(&models.KnowledgeEntry{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("partial batch saved", count, err)
	}
	second["answer"] = "Ready"
	call(422, "POST", "/knowledge-entry-batches", map[string]any{"entries": []any{map[string]any{"question": "No app", "answer": "Missing"}}}, token)
	call(401, "POST", "/knowledge-entry-batches", batch, "")
	saved := call(201, "POST", "/knowledge-entry-batches", batch, token)["data"].([]any)
	id := uint(saved[0].(map[string]any)["id"].(float64))
	entryPath := fmt.Sprintf("/knowledge-entries/%d", id)
	detail := call(200, "GET", entryPath, nil, token)["data"].(map[string]any)
	if detail["status"] != "published" || detail["locale"] != "zh" {
		t.Fatal(detail)
	}
	call(422, "PATCH", entryPath, map[string]any{"answer": "", "version": 1}, token)
	call(200, "PATCH", entryPath, map[string]any{"keywords": "", "status": "disabled", "version": 1}, token)
	call(409, "PATCH", entryPath, map[string]any{"status": "published", "version": 1}, token)
	call(422, "PATCH", entryPath, map[string]any{"question": nil, "version": 2}, token)
	filtered := call(200, "GET", fmt.Sprintf("/knowledge-entries?source_id=%d", sourceID), nil, token)
	if filtered["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatal("source filter failed", filtered)
	}
	call(201, "POST", sourcePath+"/imports", map[string]any{}, token)
	call(409, "POST", sourcePath+"/imports", map[string]any{}, token)
	call(409, "PATCH", sourcePath, map[string]any{"content": "Changed during import"}, token)
	queued := call(200, "GET", fmt.Sprintf("/knowledge-sources?app_id=%d&status=queued&name=Guide", app.ID), nil, token)
	if queued["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatal("source state filter", queued)
	}
	role := models.Role{Name: "Knowledge editor", Code: "knowledge_editor", DataScope: models.ScopeSelf}
	if err = a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dawn.knowledge.list", "dawn.knowledge.create", "dawn.knowledge.update", "dawn.knowledge.delete"} {
		if err = a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	editor := models.AdminUser{Username: "knowledge-scoped", Roles: []models.Role{role}}
	if err = a.DB.Create(&editor).Error; err != nil {
		t.Fatal(err)
	}
	_, editorToken := testSession(t, a, editor.ID)
	call(404, "GET", entryPath, nil, editorToken)
	call(404, "GET", sourcePath, nil, editorToken)
	call(404, "POST", "/knowledge-entry-batches", batch, editorToken)
	call(404, "DELETE", fmt.Sprintf("/knowledge-entries?ids=%d,%d", id, uint(saved[1].(map[string]any)["id"].(float64))), nil, editorToken)
	call(204, "DELETE", entryPath, nil, token)
	call(404, "GET", entryPath, nil, token)
	call(204, "DELETE", sourcePath, nil, token)
}
