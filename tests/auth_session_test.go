package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/authsession"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/identity"
)

func TestRedisLoginSessions(t *testing.T) {
	redis := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + redis.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err = identity.Install(context.Background(), a.DB); err != nil {
		t.Fatal(err)
	}
	if a.DB.Migrator().HasTable("admin_sessions") || a.DB.Migrator().HasTable("user_sessions") || !a.DB.Migrator().HasTable("support_sessions") {
		t.Fatal("incorrect initial schema")
	}
	users := []models.AdminUser{{Username: "super", Super: true}, {Username: "staff"}, {Username: "other"}}
	for i := range users {
		if err = a.DB.Create(&users[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	role := models.Role{Name: "sessions", Code: "sessions", Status: models.Enabled}
	if err = a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Model(&users[1]).Association("Roles").Append(&role); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dawn.users.list", "dawn.users.update"} {
		if err = a.DB.Create(&models.RolePermission{RoleID: role.ID, Key: key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	root, rootToken := testSession(t, a, users[0].ID)
	staff, staffToken := testSession(t, a, users[1].ID)
	other, otherToken := testSession(t, a, users[2].ID)
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token string, input any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var body map[string]any
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return res.Code, body
	}
	status, body := call("GET", "/sessions", staffToken, nil)
	if status != 200 {
		t.Fatal(status, body)
	}
	rows := body["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != staff.ID {
		t.Fatal("device scope", body)
	}
	if _, ok := body["meta"].(map[string]any)["cursor"]; !ok {
		t.Fatal("missing cursor")
	}
	raw, _ := json.Marshal(body)
	for _, secret := range []string{staffToken, staff.AccessHash, staff.RefreshHash} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("credential leaked")
		}
	}
	if code, _ := call("DELETE", "/sessions/"+other.ID, staffToken, nil); code != 403 {
		t.Fatal("foreign revoke", code)
	}
	if code, _ := call("GET", "/sessions?cursor=wrong", rootToken, nil); code != 422 {
		t.Fatal("invalid cursor", code)
	}
	if code, _ := call("DELETE", "/sessions/"+other.ID, rootToken, nil); code != 204 {
		t.Fatal(code)
	}
	if _, err = a.Authenticate(otherToken); err == nil {
		t.Fatal("revoked access accepted")
	}
	if err = a.Realtime.Validate(ctx, realtime.Principal{Realm: "admin", Subject: fmt.Sprint(users[2].ID), SessionID: other.ID}); err == nil {
		t.Fatal("revoked WS accepted")
	}

	r, access, refresh, err := a.Sessions.Create(ctx, authsession.Record{UserID: users[0].ID}, time.Hour, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var newAccess, newRefresh string
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, y, e := a.Sessions.Refresh(ctx, refresh, time.Hour, 2*time.Hour)
			if e == nil {
				if success.Add(1) == 1 {
					newAccess, newRefresh = x, y
				}
			} else if !errors.Is(e, authsession.ErrUnauthenticated) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("refresh used more than once", success.Load())
	}
	if _, err = a.Authenticate(access); err == nil {
		t.Fatal("old access accepted")
	}
	if _, err = a.Authenticate(newAccess); err != nil {
		t.Fatal(err)
	}
	if err = a.Sessions.Revoke(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Sessions.Refresh(ctx, newRefresh, time.Hour, time.Hour); !errors.Is(err, authsession.ErrUnauthenticated) {
		t.Fatal("revoked refresh", err)
	}
	for range 12 {
		record, token, renew, e := a.Sessions.Create(ctx, authsession.Record{UserID: users[2].ID}, time.Hour, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		wg.Add(2)
		go func() { defer wg.Done(); _, _, _ = a.Sessions.Refresh(ctx, renew, time.Hour, time.Hour) }()
		go func() {
			defer wg.Done()
			if e := a.Sessions.RevokeUsers(ctx, users[2].ID); e != nil {
				t.Error(e)
			}
		}()
		wg.Wait()
		if _, e = a.Sessions.Read(ctx, record.ID); !errors.Is(e, authsession.ErrUnauthenticated) {
			t.Fatal("revoke resurrected", e)
		}
		if _, e = a.Authenticate(token); e == nil {
			t.Fatal("revoked account accepted")
		}
	}

	app := models.SupportApp{Name: "demo", Config: customer.Raw(customer.Config{AIEnabled: true})}
	if err = a.Customer.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := a.Customer.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Sessions.Authenticate(ctx, visitor.Token, false); !errors.Is(err, authsession.ErrUnauthenticated) {
		t.Fatal("customer crossed admin boundary", err)
	}
	if _, err = a.Customer.Authenticate(ctx, rootToken, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("admin crossed customer boundary", err)
	}
	again, history, err := a.Customer.Open(ctx, app.PublicKey, visitor.Token, customer.OpenInput{})
	if err != nil || again.UserID != visitor.UserID || history.ID != conv.ID {
		t.Fatal("lost visitor", err)
	}
	if err = a.Customer.Sessions.RevokeUsers(ctx, visitor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Customer.Authenticate(ctx, visitor.Token, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("visitor revoke", err)
	}
	if err = a.Customer.Validate(ctx, realtime.Principal{Realm: customer.Realm(app.ID), Subject: visitor.User.UniqueID, SessionID: visitor.ID}); err == nil {
		t.Fatal("revoked customer WS accepted")
	}
	if _, err = a.ActorForSession(root.ID); err != nil {
		t.Fatal("unrelated session revoked", err)
	}
}
