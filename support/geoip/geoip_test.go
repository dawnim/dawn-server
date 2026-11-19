package geoip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
	m := New(Config{Enabled: true, Directory: filepath.Dir(target), Sources: map[string]string{"GeoLite2-City.mmdb": server.URL}})
	defer m.Close()
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := m.Lookup(context.Background(), "81.2.69.142")
	if err != nil || result.CountryCode != "GB" || result.Location == "" {
		t.Fatal(result, err)
	}
	broken = true
	if err := m.Update(context.Background()); err == nil {
		t.Fatal("接受了无效数据库")
	}
	result, err = m.Lookup(context.Background(), "81.2.69.142")
	if err != nil || result.CountryCode != "GB" {
		t.Fatal("更新失败破坏了已有库", err)
	}
	result, err = m.Lookup(context.Background(), "127.0.0.1")
	if err != nil || result.Location != "内网或保留地址" {
		t.Fatal(result, err)
	}
}
