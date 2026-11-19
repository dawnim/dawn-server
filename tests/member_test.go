package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/authsession"
)

func TestMemberResource(t *testing.T) {
	a := customerRuntime(t)
	root := models.AdminUser{Username: "root", Super: true}
	staff := models.AdminUser{Username: "staff"}
	for _, user := range []*models.AdminUser{&root, &staff} {
		if err := a.DB.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, token := testSession(t, a, root.ID)
	_, staffToken := testSession(t, a, staff.ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential string, input any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin/members"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+credential)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		if want == 201 && res.Header().Get("Location") == "" {
			t.Fatal("missing Location")
		}
		var body map[string]any
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		} else if want != 204 {
			t.Fatal("missing response body")
		}
		return body
	}
	for method, path := range map[string]string{"GET": "", "POST": "", "PATCH": "/1", "DELETE": "/1"} {
		call(method, path, staffToken, map[string]any{}, 403)
	}
	for _, input := range []map[string]any{
		{"password": "123456"},
		{"username": "short", "password": "12345"},
		{"username": "invalid", "password": "123456", "type": "ai"},
		{"username": "invalid", "password": "123456", "nickname": strings.Repeat("名", 101)},
		{"username": "invalid", "password": "123456", "email": "invalid"},
		{"username": "invalid", "password": strings.Repeat("密", 25)},
	} {
		call("POST", "", token, input, 422)
	}
	row := call("POST", "", token, map[string]any{"username": "visitor", "password": "123456", "nickname": "访客", "email": "visitor@example.com", "mobile": "123"}, 201)["data"].(map[string]any)
	id := uint(row["id"].(float64))
	path := fmt.Sprintf("/%d", id)
	if row["type"] != models.UserMember || row["unique_id"] == "" || row["password"] != nil || row["password_hash"] != nil {
		t.Fatal("incorrect public user", row)
	}
	call("GET", path, token, nil, 200)
	call("GET", "?status=bad", token, nil, 422)
	call("GET", "?type=bad", token, nil, 422)
	call("POST", "", token, map[string]any{"username": "visitor", "password": "123456"}, 409)
	row = call("PATCH", path, token, map[string]any{"nickname": ""}, 200)["data"].(map[string]any)
	if row["nickname"] != "" || row["mobile"] != "123" || row["email"] != "visitor@example.com" {
		t.Fatal("PATCH changed omitted fields", row)
	}
	for _, input := range []map[string]any{{"status": 0}, {"email": "invalid"}, {"nickname": nil}, {"unique_id": "123"}, {"avatar": strings.Repeat("a", 501)}} {
		call("PATCH", path, token, input, 422)
	}
	row = call("PUT", path, token, map[string]any{}, 200)["data"].(map[string]any)
	if row["email"] != "" || row["mobile"] != "" || row["locale"] != "zh" || row["status"] != float64(1) || row["username"] != "visitor" {
		t.Fatal("PUT defaults", row)
	}
	_, access, _, err := a.Customer.Sessions.Create(context.Background(), authsession.Record{UserID: id}, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	call("PATCH", path, token, map[string]any{"status": 2}, 200)
	if _, err := a.Customer.Sessions.Authenticate(context.Background(), access, false); err == nil {
		t.Fatal("disabled user remained signed in")
	}
	for range 11 {
		if err := a.DB.Create(&models.User{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	list := call("GET", "", token, nil, 200)
	meta := list["meta"].(map[string]any)
	if meta["page_size"] != float64(10) || meta["total"] != float64(12) || len(list["data"].([]any)) != 10 {
		t.Fatal("pagination", list)
	}
	rows := list["data"].([]any)
	if rows[0].(map[string]any)["id"].(float64) < rows[1].(map[string]any)["id"].(float64) {
		t.Fatal("default ID order")
	}
	call("DELETE", fmt.Sprintf("?ids=%d,999999", id), token, nil, 404)
	call("GET", path, token, nil, 200)
	call("DELETE", "", token, nil, 422)
	call("DELETE", path, token, nil, 204)
	call("GET", path, token, nil, 404)
	call("DELETE", fmt.Sprintf("?ids=%.0f,%.0f", rows[0].(map[string]any)["id"], rows[1].(map[string]any)["id"]), token, nil, 204)
	if call("GET", "", token, nil, 200)["meta"].(map[string]any)["total"] != float64(9) {
		t.Fatal("batch deletion did not remove all selected users")
	}
}
