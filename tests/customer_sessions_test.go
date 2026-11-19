package tests

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerClosingAndRatings(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	agent := models.AdminUser{Username: "session-agent", Super: true}
	if err := a.DB.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	app := models.SupportApp{Name: "Sessions", Config: customer.Raw(customer.Config{AIEnabled: true, AgentIDs: []uint{agent.ID}})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	outsider, _, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, visitor, 0, 0, 0, "human")
	if err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "claim")
	if err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "close")
	if err != nil {
		t.Fatal(err)
	}
	first := *conv.SupportSession
	if conv.Status != "human" || conv.AssignedAdminID != agent.ID || first.Status != "closing_pending" {
		t.Fatal("premature close", conv)
	}
	if _, err = m.DecideClosure(ctx, conv.ID, outsider, first.ID, first.Version, true); !errors.Is(err, customer.ErrForbidden) {
		t.Fatal("outsider approval", err)
	}

	if _, err = m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "还有问题"}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, true); !errors.Is(err, customer.ErrClosureExpired) {
		t.Fatal("stale approval", err)
	}
	conv, err = m.Conversation(ctx, conv.ID, visitor, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if conv.SupportSession.Status != "active" {
		t.Fatal("new message did not cancel close")
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "close")
	if err != nil {
		t.Fatal(err)
	}
	first = *conv.SupportSession
	conv, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, false)
	if err != nil || conv.Status != "human" {
		t.Fatal("decline", err)
	}
	if _, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, false); err != nil {
		t.Fatal("decline retry", err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "close")
	if err != nil {
		t.Fatal(err)
	}
	first = *conv.SupportSession
	conv, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Status != "closed" || conv.AssignedAdminID != 0 || conv.SupportSession.ClosedAt == 0 {
		t.Fatal("close confirmation", conv)
	}
	if _, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, true); err != nil {
		t.Fatal("approval retry", err)
	}
	if _, err = m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "新的咨询"}); err != nil {
		t.Fatal(err)
	}
	comment := strings.Repeat("好", 2000)
	if err = m.Rate(ctx, conv.ID, visitor, first.ID, 0, 1, comment); err != nil {
		t.Fatal("previous-round Chinese rating", err)
	}
	if err = m.Rate(ctx, conv.ID, visitor, first.ID, 0, 1, comment); err != nil {
		t.Fatal("rating retry", err)
	}
	if err = m.Rate(ctx, conv.ID, visitor, first.ID, 0, 2, comment); !errors.Is(err, customer.ErrRated) {
		t.Fatal("rating overwrite", err)
	}
	conv, err = m.Reception(ctx, conv.ID, visitor, 0, 0, 0, "human")
	if err != nil {
		t.Fatal(err)
	}
	if conv.SupportSession.ID == first.ID || conv.SupportSession.Round != 2 {
		t.Fatal("new session", conv)
	}
	conv, err = m.DecideClosure(ctx, conv.ID, visitor, first.ID, first.Version, true)
	if err != nil || conv.Status != "waiting" {
		t.Fatal("old approval changed new session", err)
	}
}

func TestCustomerRetryReply(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	app := models.SupportApp{Name: "Retry", Config: customer.Raw(customer.Config{AIEnabled: true})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "问题"})
	if err != nil {
		t.Fatal(err)
	}
	var run models.AIRun
	if err = a.DB.Where("message_id = ?", message.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Model(&run).Update("status", "failed").Error; err != nil {
		t.Fatal(err)
	}
	if err = m.RetryReply(ctx, conv.ID, visitor, run.ReplyID); err != nil {
		t.Fatal(err)
	}
	key := m.Key(constants.TempAIStream, run.ReplyID)
	if err = m.Redis.Set(ctx, key, "new-stream", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err = m.RetryReply(ctx, conv.ID, visitor, run.ReplyID); err != nil {
		t.Fatal(err)
	}
	if value, _ := m.Redis.Get(ctx, key).Result(); value != "new-stream" {
		t.Fatal("duplicate retry cleared active stream")
	}
	var count int64
	if err = a.DB.Model(&models.ChatMessage{}).Where("conversation_id = ? AND sender_realm = ?", conv.ID, customer.Realm(app.ID)).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("retry duplicated user message", count, err)
	}
	if _, err = m.Reception(ctx, conv.ID, visitor, 0, 0, 0, "human"); err != nil {
		t.Fatal(err)
	}
	if err = m.RetryReply(ctx, conv.ID, visitor, run.ReplyID); !errors.Is(err, customer.ErrConflict) {
		t.Fatal("retry after handoff", err)
	}
}

func TestCustomerDirectEndAndInlineRating(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	agent := models.AdminUser{Username: "end-agent", Super: true}
	if err := a.DB.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	app := models.SupportApp{Name: "End", Config: customer.Raw(customer.Config{AgentIDs: []uint{agent.ID}})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	row := conv.SupportSession
	conv, err = m.DecideClosure(ctx, conv.ID, visitor, row.ID, row.Version, true)
	if err != nil || conv.Status != "closed" {
		t.Fatal("visitor could not end waiting session", err)
	}
	if _, err = m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "新问题"}); err != nil {
		t.Fatal(err)
	}
	conv, err = m.Conversation(ctx, conv.ID, visitor, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "claim")
	if err != nil {
		t.Fatal(err)
	}
	conv, err = m.Reception(ctx, conv.ID, customer.Session{}, agent.ID, 0, conv.Version, "close")
	if err != nil {
		t.Fatal(err)
	}
	row = conv.SupportSession
	if err = m.Rate(ctx, conv.ID, visitor, row.ID, row.Version, 1, "差评"); err != nil {
		t.Fatal("inline rating", err)
	}
	conv, err = m.Conversation(ctx, conv.ID, visitor, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if conv.Status != "closed" || conv.AssignedAdminID != 0 || conv.SupportSession.Rating == nil || *conv.SupportSession.Rating != 1 {
		t.Fatal("rating did not close session atomically", conv)
	}
	if err = m.Rate(ctx, conv.ID, visitor, row.ID, row.Version, 1, "差评"); err != nil {
		t.Fatal("rating retry", err)
	}
}
