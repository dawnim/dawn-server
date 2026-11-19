package tests

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerMemberConcurrency(t *testing.T) {
	if os.Getenv("DAWN_CUSTOMER_TEST_DSN") == "" {
		t.Skip("需要专用MariaDB空库")
	}
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	app := models.SupportApp{Name: "Concurrent", Config: customer.Raw(customer.Config{InputPreview: true})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "开始"})
	if err != nil {
		t.Fatal(err)
	}
	principal := realtime.Principal{Realm: customer.Realm(app.ID), Subject: visitor.User.UniqueID, SessionID: visitor.ID}
	for i := 1; i <= 20; i++ {
		var wg sync.WaitGroup
		errors := make(chan error, 3)
		wg.Add(3)
		go func() {
			defer wg.Done()
			errors <- m.PreviewInput(ctx, principal, conv.ID, "正在输入😀", uint64(i))
		}()
		go func() { defer wg.Done(); errors <- m.Read(ctx, conv.ID, visitor, 0, message.ID) }()
		var sent models.ChatMessage
		go func() {
			defer wg.Done()
			var e error
			sent, e = m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: fmt.Sprint("你好😀", i)})
			errors <- e
		}()
		wg.Wait()
		close(errors)
		for e := range errors {
			if e != nil {
				t.Fatal("并发输入/已读/发送", e)
			}
		}
		message = sent
	}
}
