package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerIdentityHTTP(t *testing.T) {
	a := customerRuntime(t)
	app := models.SupportApp{Name: "Public identity", Config: customer.Raw(customer.Config{Entries: []customer.Entry{{Key: "default", Origins: []string{"*"}}}})}
	if err := a.Customer.CreateApp(context.Background(), &app, customer.Secrets{IdentitySecret: "test-secret"}); err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, token string, input any, want int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(input)
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://any-website.example")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("wildcard origin unavailable")
		}
		if res.Code != want {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := "/api/support/" + app.PublicKey
	result := call(base+"/identities", "", map[string]any{}, 201)["data"].(map[string]any)
	token := result["token"].(string)
	var count int64
	a.DB.Model(&models.ChatConversation{}).Count(&count)
	if count != 0 {
		t.Fatal("initializing widget caused queue entry")
	}
	ticket := call(base+"/visitor-tickets", token, map[string]any{}, 201)["data"].(map[string]any)["ticket"]
	proof, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "website", "sub": "server", "scope": "visitor_exchange", "aud": app.PublicKey, "exp": time.Now().Add(time.Minute).Unix(), "jti": rand.Text()}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	exchanged := call(base+"/visitor-ticket-exchanges", proof, map[string]any{"ticket": ticket}, 201)["data"].(map[string]any)
	if exchanged["visitor_id"] != result["user"].(map[string]any)["unique_id"] || exchanged["user_id"] != nil {
		t.Fatal("external exchange leaked internal ID or lost guest")
	}

	call(base+"/visitor-ticket-exchanges", "", map[string]any{"ticket": "invalid"}, 401)
	call("/api/account/registrations", token, map[string]any{"username": "unsafe", "password": "test-password"}, 401)
	call(base+"/sessions", token, map[string]any{}, 201)
	a.DB.Model(&models.ChatConversation{}).Count(&count)
	if count != 1 {
		t.Fatal("opening chat did not create one consultation")
	}
}
