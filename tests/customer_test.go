package tests

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/identity"
	"github.com/golang-jwt/jwt/v5"
)

func TestCustomerIdentityAndReception(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = identity.Install(context.Background(), a.DB); err != nil {
		t.Fatal(err)
	}
	m := a.Customer
	agent := models.AdminUser{Username: "agent", Super: true}
	if err = a.DB.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	cfg := customer.Config{AllowLocalUsers: true, AIEnabled: true, InputPreview: true, AgentIDs: []uint{agent.ID}}
	app := models.SupportApp{Name: "Demo", Config: customer.Raw(cfg)}
	if err = m.CreateApp(ctx, &app, customer.Secrets{IdentitySecret: "external-signing-secret"}); err != nil {
		t.Fatal(err)
	}
	other := models.SupportApp{Name: "Other", Config: customer.Raw(cfg)}
	if err = m.CreateApp(ctx, &other, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{Attributes: map[string]any{"user_id": 999, "nick": "claimed"}})
	if err != nil {
		t.Fatal(err)
	}
	if visitor.Verified || visitor.UserID == 999 || visitor.Token == "" || len(visitor.User.UniqueID) != 7 {
		t.Fatal("untrusted identity accepted", visitor)
	}
	again, same, err := m.Open(ctx, app.PublicKey, visitor.Token, customer.OpenInput{})
	if err != nil || again.UserID != visitor.UserID || same.ID != conv.ID {
		t.Fatal("visitor recovery", err)
	}
	if _, err = m.Authenticate(ctx, visitor.Token, other.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("cross-app token", err)
	}
	if err = m.Validate(ctx, realtime.Principal{Realm: customer.Realm(other.ID), Subject: visitor.User.UniqueID, SessionID: visitor.ID}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("cross-app WS", err)
	}
	outsider, _, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Conversation(ctx, conv.ID, outsider, 0, ""); !errors.Is(err, customer.ErrForbidden) {
		t.Fatal("visitor history leak", err)
	}
	message, err := m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "如何使用产品", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	var run models.AIRun
	if err = a.DB.Where("message_id = ?", message.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Model(&run).Update("status", "running").Error; err != nil {
		t.Fatal(err)
	}
	if err = m.Redis.Set(ctx, m.Key(constants.TempAIStream, run.ReplyID), string(customer.Raw(map[string]any{"content": "正在生成的建议"})), time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, visitor, 0, 0, 0, "human")
	if err != nil || conv.Status != "waiting" {
		t.Fatal(conv, err)
	}
	if err = a.DB.First(&run, run.ID).Error; err != nil || run.Status != "cancelled" {
		t.Fatal("late AI run remains active", err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "claim")
	if err != nil || conv.AssignedAdminID != agent.ID {
		t.Fatal(conv, err)
	}
	public, _, err := m.History(ctx, conv.ID, visitor, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range public {
		if message.Visibility != "public" {
			t.Fatal("internal draft exposed")
		}
	}
	internal, _, err := m.History(ctx, conv.ID, customer.Session{}, agent.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range internal {
		found = found || message.Visibility == "draft" && message.Content == "正在生成的建议"
	}
	if !found {
		t.Fatal("handoff lost active draft")
	}
	if _, err = m.Send(ctx, conv.ID, customer.Session{}, agent.ID, customer.MessageInput{Version: conv.Version - 1, RequestID: rand.Text(), Content: "stale", Visibility: "public"}); !errors.Is(err, customer.ErrConflict) {
		t.Fatal("stale reception write", err)
	}
	if _, err = m.Send(ctx, conv.ID, customer.Session{}, agent.ID, customer.MessageInput{Version: conv.Version, RequestID: rand.Text(), Content: "人工回复", Visibility: "public"}); err != nil {
		t.Fatal(err)
	}
	p := realtime.Principal{Realm: customer.Realm(app.ID), Subject: visitor.User.UniqueID, SessionID: visitor.ID}
	if err = m.PreviewInput(ctx, p, conv.ID, "未发送文本", 2); err != nil {
		t.Fatal(err)
	}
	if err = m.PreviewInput(ctx, p, conv.ID, "旧文本", 1); err != nil {
		t.Fatal(err)
	}
	var member models.ChatMember
	if err = a.DB.Where("conversation_id = ? AND realm = ?", conv.ID, p.Realm).First(&member).Error; err != nil {
		t.Fatal(err)
	}
	var preview customer.Preview
	if err = json.Unmarshal(member.Extra, &preview); err != nil || preview.Content != "未发送文本" {
		t.Fatal("preview ordering", err)
	}
	memberSession, err := m.Register(ctx, customer.Session{}, "member", "secure-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := m.IdentityTicket(ctx, memberSession.UserID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	verified, merged, err := m.Open(ctx, app.PublicKey, visitor.Token, customer.OpenInput{Ticket: ticket})
	if err != nil || !verified.Verified || verified.UserID != memberSession.UserID || merged.ID != conv.ID {
		t.Fatal("local verified identity", err)
	}
	if _, err = m.Authenticate(ctx, visitor.Token, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("old guest credential retained access", err)
	}
	if _, _, err = m.Open(ctx, app.PublicKey, "", customer.OpenInput{Ticket: ticket}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("ticket replay", err)
	}
	proof, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "product", "sub": "customer-42", "aud": app.PublicKey, "exp": time.Now().Add(time.Minute).Unix(), "jti": rand.Text()}).SignedString([]byte("external-signing-secret"))
	if err != nil {
		t.Fatal(err)
	}
	external, _, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{Identity: proof})
	if err != nil || !external.Verified {
		t.Fatal("external proof", err)
	}
	if _, _, err = m.Open(ctx, app.PublicKey, "", customer.OpenInput{Identity: proof}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("proof replay", err)
	}
	if _, _, err = m.Open(ctx, other.PublicKey, "", customer.OpenInput{Identity: proof}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("proof app boundary", err)
	}
}
