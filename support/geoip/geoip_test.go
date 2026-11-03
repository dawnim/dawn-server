package geoip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/foundation"
)

func TestUpdateAndLookup(t *testing.T) {
	data, err := os.ReadFile("testdata/GeoIP2-City-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	broken := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			w.Write([]byte("broken"))
		} else {
			w.Write(data)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "City.mmdb")
	config.Add("dawn", func() map[string]any {
		return map[string]any{"geoip": map[string]any{"enabled": true, "path": target, "url": server.URL}}
	})
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, location, err := Lookup("81.2.69.142")
	if err != nil || code != "GB" || location == "" {
		t.Fatal(code, location, err)
	}
	broken = true
	if err := Update(context.Background()); err == nil {
		t.Fatal("接受了无效数据库")
	}
	code, _, err = Lookup("81.2.69.142")
	if err != nil || code != "GB" {
		t.Fatal("更新失败破坏了已有库", err)
	}
	_, location, err = Lookup("127.0.0.1")
	if err != nil || location != "内网地址" {
		t.Fatal(location, err)
	}
}
