package tests

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/authsession"
)

func TestLoginSurvivesRestart(t *testing.T) {
	for _, encoding := range []string{"json", "msgpack"} {
		t.Run(encoding, func(t *testing.T) {
			redis := miniredis.RunT(t)
			dir := t.TempDir()
			options := testOptions{DBDSN: filepath.Join(dir, "test.sqlite"), Storage: dir, RedisURL: "redis://" + redis.Addr(), SessionEncoding: encoding}
			a, err := newTestApp(options)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := databaseCommand(a, "migrate"); err != nil {
				t.Fatal(err)
			}
			root := models.AdminUser{Username: "root", Super: true}
			if err := a.DB.Create(&root).Error; err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			_, access, refresh, err := a.Sessions.Create(ctx, authsession.Record{UserID: root.ID}, 30*24*time.Hour, 30*24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			a.Close()
			a, err = newTestApp(options)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if _, err := a.Authenticate(access); err != nil {
				t.Fatal("restart lost login", err)
			}
			handler, err := a.Handler()
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				resume string
				status int
			}{{`{"resume":"expired-ws-session"}`, 409}, {`{"resume":""}`, 200}} {
				req := httptest.NewRequest("POST", "/admin/realtime/ticket", strings.NewReader(test.resume))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+access)
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code != test.status {
					t.Fatalf("ticket: %d %s", res.Code, res.Body.String())
				}
			}
			if _, _, err := a.Sessions.Refresh(ctx, refresh, time.Hour, time.Hour); err != nil {
				t.Fatal("restart lost refresh", err)
			}
		})
	}
}
