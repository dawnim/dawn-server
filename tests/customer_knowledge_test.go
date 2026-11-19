package tests

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
)

func TestCustomerKnowledgeNaturalQuestions(t *testing.T) {
	a := customerRuntime(t)
	app := models.SupportApp{Name: "Dawn"}
	if err := a.Customer.CreateApp(context.Background(), &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	ip := models.KnowledgeEntry{AppID: app.ID, Question: "Dawn-IP地域查询与更新-开发者调用", Answer: "IP所属位置、地域与运营商查询。import github.com/dawnim/dawn-server/support/geoip\nresult, err := geoip.Lookup(ctx, ip)", Status: "published"}
	entries := []models.KnowledgeEntry{ip,
		{AppID: app.ID, Question: "Dawn-快速开始-安装", Answer: "安装Dawn CMS：git clone dawn-simple；go run . dawn:install", Status: "published"},
		{AppID: app.ID, Question: "PrismGo-安装", Answer: "安装PrismGo CLI", Status: "published"},
		{AppID: app.ID, Question: "保密资料", Answer: "geoip.Lookup秘密内容", Status: "draft"},
		{AppID: app.ID + 1, Question: "IP查询", Answer: "其他应用资料", Status: "published"},
	}
	if err := a.DB.Create(entries[:1]).Error; err != nil {
		t.Fatal(err)
	}
	if found, err := a.Customer.Search(context.Background(), app.ID, "geoip.Lookup"); err != nil || len(found) != 1 {
		t.Fatal("single entry body search failed", found, err)
	}
	if err := a.DB.Create(entries[1:]).Error; err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"如何获取ip所属位置啥的？", "咱们这套框架怎么查询ip地址", "geoip.Lookup"} {
		found, err := a.Customer.Search(context.Background(), app.ID, query)
		if err != nil || len(found) != 1 || found[0].ID != entries[0].ID {
			t.Fatalf("%s: %+v %v", query, found, err)
		}
	}
	found, err := a.Customer.Search(context.Background(), app.ID, "Dawn怎么安装啊")
	if err != nil || len(found) == 0 || found[0].ID != entries[1].ID {
		t.Fatalf("wrong product: %+v %v", found, err)
	}
}

func TestCustomerKnowledgeToolResearch(t *testing.T) {
	a := customerRuntime(t)
	ctx := context.Background()
	var entry models.KnowledgeEntry
	turn := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := map[string]any{"id": "test", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}
		choice := event["choices"].([]any)[0].(map[string]any)
		switch {
		case turn < 6:
			if !strings.Contains(string(customer.Raw(input["tools"])), "search_knowledge") {
				t.Error("search tool missing")
			}
			choice["delta"] = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("search_%d", turn), "type": "function", "function": map[string]any{"name": "search_knowledge", "arguments": `{"query":"geoip.Lookup"}`}}}}
		case turn == 6:
			if !strings.Contains(string(customer.Raw(input["messages"])), "geoip.Lookup(ctx, ip)") {
				t.Error("retrieved evidence not delivered")
			}
			choice["delta"] = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "cite", "type": "function", "function": map[string]any{"name": "knowledge", "arguments": fmt.Sprintf(`{"id":%d}`, entry.ID)}}}}
		default:
			choice["delta"] = map[string]any{"content": "使用geoip.Lookup(ctx, ip)查询IP所属位置"}
			choice["finish_reason"] = "stop"
		}
		turn++
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", customer.Raw(event))
	}))
	defer model.Close()
	app := models.SupportApp{Name: "Dawn", Config: customer.Raw(customer.Config{AIEnabled: true, BaseURL: model.URL, Model: "test", ModelProtocol: "chat_completions"})}
	if err := a.Customer.CreateApp(ctx, &app, customer.Secrets{APIKey: "test"}); err != nil {
		t.Fatal(err)
	}
	entry = models.KnowledgeEntry{AppID: app.ID, Question: "IP地域查询", Answer: "geoip.Lookup(ctx, ip)", Status: "published"}
	if err := a.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	session, conv, err := a.Customer.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := a.Customer.Send(ctx, conv.ID, session, 0, customer.MessageInput{RequestID: rand.Text(), Content: "我刚才问的是这个功能的用法"})
	if err != nil {
		t.Fatal(err)
	}
	var run models.AIRun
	if err = a.DB.Where("message_id = ?", message.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.Customer.ProcessRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.First(&run, run.ID).Error; err != nil || run.Status != "queued" {
		t.Fatal("research did not checkpoint", run.Status, err)
	}
	if err = a.Customer.ProcessRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var reply models.ChatMessage
	if err = a.DB.First(&reply, run.ReplyID).Error; err != nil {
		t.Fatal(err)
	}
	if turn != 8 || !strings.Contains(string(reply.Payload), `"id":`+fmt.Sprint(entry.ID)) || !strings.Contains(reply.Content, "geoip.Lookup") {
		t.Fatalf("research/citation failed: %d %+v", turn, reply)
	}
}
