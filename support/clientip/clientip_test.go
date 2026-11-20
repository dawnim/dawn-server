package clientip

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/container"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/framework/route"
)

func TestIP(t *testing.T) {
	tests := []struct {
		name, mode, remote, headers, value, want string
		proxies                                  []string
	}{
		{"untrusted ignores spoof", "trusted", "203.0.113.1:80", "X-Forwarded-For", "1.1.1.1", "203.0.113.1", nil},
		{"proxy chain", "trusted", "127.0.0.1:80", "X-Forwarded-For", "9.9.9.9, 198.51.100.2, 10.0.0.1", "198.51.100.2", []string{"127.0.0.1", "10.0.0.0/8"}},
		{"invalid chain boundary", "trusted", "127.0.0.1:80", "X-Forwarded-For", "1.1.1.1, invalid, 10.0.0.1", "10.0.0.1", []string{"127.0.0.1", "10.0.0.0/8"}},
		{"custom header", "trusted", "127.0.0.1:80", " X-Real-IP ", "8.8.8.8", "8.8.8.8", []string{"127.0.0.1"}},
		{"forwarded ipv6", "trusted", "127.0.0.1:80", "Forwarded", `for="[2001:4860:4860::8888]:443";proto=https`, "2001:4860:4860::8888", []string{"127.0.0.1"}},
		{"simple public", "simple", "127.0.0.1:80", "X-Forwarded-For", "10.0.0.1, 8.8.8.8", "8.8.8.8", nil},
		{"simple fallback", "simple", "127.0.0.1:80", "X-Forwarded-For", "10.0.0.1", "10.0.0.1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New(Config{Mode: tt.mode, Headers: []string{tt.headers}, TrustedProxies: tt.proxies})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			req.Header.Set(strings.TrimSpace(tt.headers), tt.value)
			if got := r.IP(req); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMiddlewareOverwritesClientHeader(t *testing.T) {
	r, err := New(Config{Mode: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	services := container.NewContainer()
	if err := services.Instance("dawn.clientip", r); err != nil {
		t.Fatal(err)
	}
	container.SetProvider(func() *container.Container { return services })
	defer container.SetProvider(nil)
	engine := gin.New()
	Register(engine)
	engine.GET("/", func(c *gin.Context) { c.String(200, c.ClientIP()) })
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	req.Header.Set(resolvedHeader, "1.1.1.1")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Body.String() != "203.0.113.1" {
		t.Fatal(w.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestUpdateRetainsLastGoodSnapshot(t *testing.T) {
	r, err := New(Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.config.Cloudflare = true
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "1.1.1.0/24"
		if strings.HasSuffix(req.URL.Path, "v6") {
			body = "2606:4700::/32"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	r.Update(context.Background())
	if len(r.snapshot.Load().cloudflare) != 2 {
		t.Fatal("missing ranges")
	}
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	r.Update(context.Background())
	if len(r.snapshot.Load().cloudflare) != 2 || r.Status()["cloudflare"].Error == "" {
		t.Fatal("failed update replaced snapshot or lost error")
	}
}

func TestCloudFrontUpdateAndCache(t *testing.T) {
	directory := t.TempDir()
	r, err := New(Config{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.config.CloudFront = true
	body := `{"prefixes":[{"ip_prefix":"203.0.113.0/24","service":"CLOUDFRONT_ORIGIN_FACING"}],"ipv6_prefixes":[{"ipv6_prefix":"2001:db8::/32","service":"CLOUDFRONT_ORIGIN_FACING"}]}`
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	r.Update(context.Background())
	if got := r.Status()["cloudfront"]; got.Count != 2 || got.Error != "" {
		t.Fatal(got)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.4:1234"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := r.IP(req); got != "8.8.8.8" {
		t.Fatal(got)
	}
	body = `{"prefixes":[{"ip_prefix":"203.0.113.0/24","service":"CLOUDFRONT_ORIGIN_FACING"}]}`
	r.Update(context.Background())
	if got := r.Status()["cloudfront"]; got.Count != 2 || got.Error == "" {
		t.Fatal(got)
	}
	cached, err := New(Config{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer cached.Close()
	cached.config.CloudFront = true
	if got := cached.IP(req); got != "8.8.8.8" {
		t.Fatal(got)
	}
}

func TestHeaderPriorityAndConfigErrors(t *testing.T) {
	if _, err := New(Config{Mode: "invalid"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := New(Config{TrustedProxies: []string{"invalid"}}); err == nil {
		t.Fatal("invalid proxy accepted")
	}
	r, err := New(Config{Mode: "simple", Headers: []string{"CF-Connecting-IP", "X-Forwarded-For"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("CF-Connecting-IP", "2001:4860:4860::8888")
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := r.IP(req); got != "2001:4860:4860::8888" {
		t.Fatal(got)
	}
}

func TestFrameworkHTTPEntry(t *testing.T) {
	resolver, err := New(Config{Mode: "trusted", Headers: []string{"X-Forwarded-For"}, TrustedProxies: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	app := foundation.Configure(t.TempDir()).WithMiddleware(func(m *foundation.Middleware) { m.Prepend(Register) }).WithRouting(func(r *foundation.Routing) {
		r.Routes(func(_ *foundation.Application, engine *gin.Engine) error {
			engine.GET("/ip", func(c *gin.Context) { c.String(200, c.ClientIP()) })
			return nil
		})
	}).Create()
	defer app.Close()
	if err := (route.ServiceProvider{}).Register(app); err != nil {
		t.Fatal(err)
	}
	if err := app.Container().Instance("dawn.clientip", resolver); err != nil {
		t.Fatal(err)
	}
	server, err := app.NewHTTPServer(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/ip", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set(resolvedHeader, "1.1.1.1")
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "8.8.8.8" {
		t.Fatalf("status %d, IP %q", w.Code, w.Body.String())
	}
}
