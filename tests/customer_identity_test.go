package tests

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/golang-jwt/jwt/v5"
)

func TestCustomerRegistrationPasswordLength(t *testing.T) {
	m := customerRuntime(t).Customer
	if _, err := m.Register(context.Background(), customer.Session{}, "six-char-user", "123456", "Member"); err != nil {
		t.Fatal("six-character password rejected", err)
	}
	if _, err := m.Register(context.Background(), customer.Session{}, "five-char-user", "12345", "Member"); !errors.Is(err, customer.ErrInvalid) {
		t.Fatal("five-character password accepted", err)
	}
}

func TestCustomerBidirectionalIdentity(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	app := models.SupportApp{Name: "Identity", Config: customer.Raw(customer.Config{AllowLocalUsers: true, Entries: []customer.Entry{{Key: "default", Origins: []string{"*"}}}})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{IdentitySecret: "identity-test-secret"}); err != nil {
		t.Fatal(err)
	}
	guest, err := m.Identify(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	a.DB.Model(&models.ChatConversation{}).Count(&count)
	if count != 0 {
		t.Fatal("identity initialization created a consultation")
	}
	reverse, err := m.VisitorTicket(ctx, guest)
	if err != nil {
		t.Fatal(err)
	}
	local, ticket, err := m.LocalIdentity(ctx, app.ID, "", reverse)
	if err != nil || local.UserID != guest.UserID {
		t.Fatal("reverse local identity", err)
	}
	retry, _, err := m.LocalIdentity(ctx, app.ID, "", reverse)
	if err != nil || retry.UserID != guest.UserID {
		t.Fatal("reverse retry created another user", err)
	}
	trusted, conv, err := m.Open(ctx, app.PublicKey, guest.Token, customer.OpenInput{Ticket: ticket})
	if err != nil || !trusted.Verified || trusted.User.Type != models.UserGuest || trusted.UserID != local.UserID {
		t.Fatal("forward local guest identity", err)
	}
	if _, err = m.Authenticate(ctx, guest.Token, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("anonymous token survived trusted binding", err)
	}
	member, err := m.Register(ctx, local, "registered-user", "test-password", "Registered")
	if err != nil || member.UserID != guest.UserID || member.User.UniqueID != guest.User.UniqueID {
		t.Fatal("registration changed identity", err)
	}
	for _, s := range []customer.Session{local, retry, trusted} {
		if _, err = m.Authenticate(ctx, s.Token, s.AppID); !errors.Is(err, customer.ErrUnauthenticated) {
			t.Fatal("old guest credential survived upgrade", err)
		}
	}
	if err = m.Validate(ctx, realtime.Principal{Realm: customer.Realm(app.ID), Subject: trusted.User.UniqueID, SessionID: trusted.ID}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("old guest WS survived upgrade", err)
	}
	if _, err = m.ClaimVisitor(ctx, app.ID, reverse, "__local"); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("old reverse ticket claimed registered account", err)
	}
	ticket, err = m.IdentityTicket(ctx, member.UserID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, currentConv, err := m.Open(ctx, app.PublicKey, trusted.Token, customer.OpenInput{Ticket: ticket})
	if err != nil || currentConv.ID != conv.ID || current.User.Type != models.UserMember {
		t.Fatal("upgraded conversation", err)
	}
	if _, err = m.VisitorTicket(ctx, current); !errors.Is(err, customer.ErrForbidden) {
		t.Fatal("member minted guest ticket", err)
	}
	same, ticket, err := m.LocalIdentity(ctx, app.ID, member.Token, "")
	if err != nil || same.UserID != member.UserID {
		t.Fatal(err)
	}
	stable, err := m.Identify(ctx, app.PublicKey, current.Token, customer.OpenInput{Ticket: ticket})
	if err != nil || stable.ID != current.ID {
		t.Fatal("identity refresh rotated an unchanged session", err)
	}

	sign := func(subject, kind, scope string) string {
		t.Helper()
		proof, e := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "shop", "sub": subject, "type": kind, "scope": scope, "aud": app.PublicKey, "exp": time.Now().Add(time.Minute).Unix(), "jti": rand.Text(), "user": map[string]any{"nickname": "Website user"}}).SignedString([]byte("identity-test-secret"))
		if e != nil {
			t.Fatal(e)
		}
		return proof
	}
	externalGuest, err := m.Identify(ctx, app.PublicKey, "", customer.OpenInput{Identity: sign("visitor-42", "guest", "")})
	if err != nil || !externalGuest.Verified || externalGuest.User.Type != models.UserGuest || externalGuest.User.Nickname != "Website user" {
		t.Fatal("external visitor", err)
	}
	externalTicket, err := m.VisitorTicket(ctx, externalGuest)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := m.ExchangeVisitor(ctx, app.PublicKey, externalTicket, sign("server", "guest", "visitor_exchange"))
	if err != nil || exchange["external_user_id"] != "visitor-42" || exchange["visitor_id"] != externalGuest.User.UniqueID {
		t.Fatal("reverse external identity", exchange, err)
	}
	if _, err = m.ClaimVisitor(ctx, app.ID, externalTicket, "other-site"); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("ticket crossed receiver", err)
	}
	if _, err = m.ExchangeVisitor(ctx, app.PublicKey, externalTicket, sign("server", "guest", "")); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("ordinary login proof authorized backend exchange", err)
	}
	externalMember, err := m.Identify(ctx, app.PublicKey, externalGuest.Token, customer.OpenInput{Identity: sign("visitor-42", "member", "")})
	if err != nil || externalMember.UserID != externalGuest.UserID || externalMember.User.Type != models.UserMember {
		t.Fatal("external promotion", err)
	}
	if _, err = m.Identify(ctx, app.PublicKey, "", customer.OpenInput{Identity: sign("visitor-42", "guest", "")}); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("guest proof recovered registered account", err)
	}
	if _, err = m.Authenticate(ctx, externalGuest.Token, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("old external guest credential survived upgrade", err)
	}

	tool := customer.Tool{Name: "member_only", Driver: "go", RequireAccount: true, Parameters: map[string]any{"type": "object"}}
	if _, err = m.ExecuteTool(ctx, customer.ToolActor{Verified: true, UserType: models.UserGuest}, tool, map[string]any{}, false); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal("verified guest obtained member tool", err)
	}
}

func TestCustomerMergeGuestHistory(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	app := models.SupportApp{Name: "Merge", Config: customer.Raw(customer.Config{AllowLocalUsers: true})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	guest, first, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	second := models.ChatConversation{Type: "support", AppID: app.ID, UserID: guest.UserID, Identify: rand.Text(), Status: "closed", Round: 2, Version: 1}
	if err = a.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	member, err := m.Register(ctx, customer.Session{}, "existing-account", "test-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := m.IdentityTicket(ctx, member.UserID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.Identify(ctx, app.PublicKey, guest.Token, customer.OpenInput{Ticket: ticket})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{first.ID, second.ID} {
		var conv models.ChatConversation
		if err = a.DB.First(&conv, id).Error; err != nil || conv.UserID != member.UserID {
			t.Fatal("lost guest history", id, err)
		}
	}
	if _, err = m.Conversation(ctx, first.ID, session, 0, ""); err != nil {
		t.Fatal(err)
	}
	_, selected, err := m.Open(ctx, app.PublicKey, session.Token, customer.OpenInput{ConversationID: first.ID})
	if err != nil || selected.ID != first.ID {
		t.Fatal("identity synchronization switched current conversation", err)
	}
	outsider, foreign, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	_, selected, err = m.Open(ctx, app.PublicKey, outsider.Token, customer.OpenInput{ConversationID: first.ID})
	if err != nil || selected.ID != foreign.ID {
		t.Fatal("conversation selector crossed user boundary", err)
	}

	if _, err = m.Authenticate(ctx, guest.Token, app.ID); !errors.Is(err, customer.ErrUnauthenticated) {
		t.Fatal(err)
	}
}
