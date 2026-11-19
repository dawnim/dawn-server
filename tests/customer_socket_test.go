package tests

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/gorilla/websocket"
)

func TestCustomerVisitorWebSocket(t *testing.T) {
	a := customerRuntime(t)
	ctx := context.Background()
	app := models.SupportApp{Name: "Public websocket"}
	if err := a.Customer.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	session, conv, err := a.Customer.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := a.Customer.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	principal := realtime.Principal{Realm: customer.Realm(app.ID), Subject: session.User.UniqueID, SessionID: session.ID}
	ticket, err := a.Realtime.Ticket(ctx, principal, "", "zh")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Realtime)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err = conn.WriteJSON(map[string]any{"type": "auth", "ticket": ticket}); err != nil {
		t.Fatal(err)
	}
	var packet realtime.Message
	if err = conn.ReadJSON(&packet); err != nil || packet.Type != "ready" {
		t.Fatal("public identity handshake", packet.Type, err)
	}
	message, err := a.Customer.Send(ctx, conv.ID, session, 0, customer.MessageInput{Content: "hello", RequestID: rand.Text()})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PublishChatEvents(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		if err = conn.ReadJSON(&packet); err != nil {
			t.Fatal(err)
		}
		if packet.Type != "chat.message.created" {
			continue
		}
		raw, _ := json.Marshal(packet.Data)
		var event struct {
			Data models.ChatMessage `json:"data"`
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Data.ID != message.ID {
			continue
		}
		if event.Data.SenderSubject != session.User.UniqueID {
			t.Fatal("message exposed internal user subject", event.Data.SenderSubject)
		}
		break
	}
	forbidden := realtime.Principal{Realm: principal.Realm, Subject: other.User.UniqueID, SessionID: other.ID}
	event := realtime.Message{Type: "chat.message.created", Data: customer.Raw(message)}
	if err = a.Realtime.AuthorizeEvent(ctx, forbidden, event); err == nil {
		t.Fatal("same-app visitor could receive another visitor's message")
	}
}
