package tests

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/identity"
	"github.com/prismgo/framework/encryption"
	"gorm.io/gorm"
)

func customerRuntime(t *testing.T) *admin.App {
	t.Helper()
	store := miniredis.RunT(t)
	options := testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	if os.Getenv("DAWN_CUSTOMER_TEST_DSN") != "" {
		options.DBDriver = "mariadb"
		options.DBDSN = os.Getenv("DAWN_CUSTOMER_TEST_DSN")
	}
	a, err := newTestApp(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err = databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err = identity.Install(context.Background(), a.DB); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCustomerToolsAndConfirmation(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	tool := customer.Tool{Name: "confirm_change", Driver: "go", Description: "修改自己的资料", Confirmation: true, Parameters: map[string]any{"type": "object", "properties": map[string]any{"nickname": map[string]any{"type": "string"}}, "required": []string{"nickname"}, "additionalProperties": false}}
	app := models.SupportApp{Name: "Tools", Config: customer.Raw(customer.Config{Tools: []customer.Tool{tool}})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	session, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	outsider, _, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Int32
	var idempotency string
	m.RegisterTool(tool.Name, func(ctx context.Context, actor customer.ToolActor, args map[string]any) (any, error) {
		called.Add(1)
		idempotency = actor.IdempotencyKey
		return map[string]any{"updated": true}, nil
	})
	actor := customer.ToolActor{AppID: app.ID, UserID: session.UserID, ConversationID: conv.ID, Version: conv.Version}
	output, err := m.ExecuteTool(ctx, actor, tool, map[string]any{"nickname": "New name"}, false)
	if err != nil {
		t.Fatal(err)
	}
	actionID := output.(map[string]any)["run_id"].(uint)
	if called.Load() != 0 {
		t.Fatal("write executed without confirmation")
	}
	if err = m.DecideAction(ctx, actionID, outsider, 0, true); !errors.Is(err, customer.ErrForbidden) {
		t.Fatal("cross-user approval", err)
	}
	if err = m.DecideAction(ctx, actionID, session, 0, true); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 || idempotency == "" {
		t.Fatal("missing execution or idempotency key")
	}
	if err = m.DecideAction(ctx, actionID, session, 0, true); !errors.Is(err, customer.ErrConflict) {
		t.Fatal("duplicate approval", err)
	}
	if called.Load() != 1 {
		t.Fatal("approval executed twice")
	}
	actor.Public = true
	if _, err = m.ExecuteTool(ctx, actor, tool, map[string]any{"nickname": "Group"}, false); !errors.Is(err, customer.ErrForbidden) {
		t.Fatal("private tool exposed in group", err)
	}
	actor.Public = false
	if _, err = m.ExecuteTool(ctx, actor, tool, map[string]any{"nickname": "New", "user_id": 999}, false); !errors.Is(err, customer.ErrInvalid) {
		t.Fatal("schema allowed an undeclared owner", err)
	}
	type order struct {
		ID     uint
		UserID uint
		AppID  uint
		Amount int
		Secret string
	}
	if err = a.DB.Table("test_orders").AutoMigrate(&order{}); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Table("test_orders").Create(&[]order{{UserID: session.UserID, AppID: app.ID, Amount: 10, Secret: "private"}, {UserID: outsider.UserID, AppID: app.ID, Amount: 20, Secret: "other"}, {UserID: session.UserID, AppID: app.ID + 1, Amount: 30, Secret: "another-app"}}).Error; err != nil {
		t.Fatal(err)
	}
	m.RegisterQuery("orders", customer.Query{Table: "test_orders", Columns: []string{"id", "amount", "user_id"}, UserColumn: "user_id", AppColumn: "app_id", Filters: []string{"user_id"}})
	query := customer.Tool{Name: "orders", Driver: "sql", RequireIdentity: true, Parameters: map[string]any{"type": "object", "properties": map[string]any{"user_id": map[string]any{"type": "number"}}, "additionalProperties": false}}
	actor.Verified = true
	output, err = m.ExecuteTool(ctx, actor, query, map[string]any{}, false)
	if err != nil {
		t.Fatal(err)
	}
	data := output.(map[string]any)["data"].([]map[string]any)
	if len(data) != 1 || fmt.Sprint(data[0]["amount"]) != "10" {
		t.Fatal("SQL scope", output)
	}
	if _, exists := data[0]["secret"]; exists {
		t.Fatal("SQL returned a non-whitelisted column")
	}
	output, err = m.ExecuteTool(ctx, actor, query, map[string]any{"user_id": float64(outsider.UserID)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.(map[string]any)["data"].([]map[string]any)) != 0 {
		t.Fatal("model replaced the owner scope")
	}
}

func TestCustomerEmbeddingSwitch(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	dimensions := 2
	broken := false
	var embeddingKey atomic.Value
	embeddingKey.Store("")
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if broken {
			http.Error(w, "offline", 503)
			return
		}
		embeddingKey.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer answering-key" {
			t.Error("answering credential leaked to embedding service")
		}
		var input struct {
			Dimensions int `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		vector := make([]float64, dimensions)
		vector[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}, "model": "embedding-model", "object": "list"})
	}))
	defer service.Close()
	cfg := customer.Config{EmbeddingURL: service.URL, EmbeddingModel: "model-a", EmbeddingDimensions: 2, IndexDriver: "portable"}
	if a.DB.Dialector.Name() == "mysql" {
		cfg.IndexDriver = "mariadb"
	}
	app := models.SupportApp{Name: "Index", Config: customer.Raw(cfg)}
	if err := m.CreateApp(ctx, &app, customer.Secrets{APIKey: "answering-key", EmbeddingAPIKey: "embedding-key"}); err != nil {
		t.Fatal(err)
	}
	entry := models.KnowledgeEntry{AppID: app.ID, Question: "配置登录", Answer: "使用登录接口", Status: "published"}
	if err := a.DB.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.BuildIndex(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	_, current, _, err := m.App(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := *current.ActiveIndex
	cfg = current
	credential, err := encryption.EncryptString(ctx, string(customer.Raw(customer.Secrets{APIKey: "answering-key", EmbeddingAPIKey: "next-embedding-key"})))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Model(&app).Update("secrets", credential).Error; err != nil {
		t.Fatal(err)
	}
	if found, err := m.Search(ctx, app.ID, "only-vector-match"); err != nil || len(found) != 1 || embeddingKey.Load() != "Bearer embedding-key" {
		t.Fatal("live index lost its original embedding credential", found, err, embeddingKey.Load())
	}

	cfg.EmbeddingModel = "model-b"
	cfg.EmbeddingDimensions = 3
	dimensions = 3
	if err = a.DB.Model(&app).Updates(map[string]any{"config": customer.Raw(cfg), "revision": gorm.Expr("revision + 1")}).Error; err != nil {
		t.Fatal(err)
	}
	if err = m.BuildIndex(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	_, current, _, err = m.App(ctx, app.ID)
	if err != nil || current.ActiveIndex.Dimensions != 3 || current.ActiveIndex.Profile == first.Profile {
		t.Fatal("index generation was reused", err)
	}
	active := current.ActiveIndex.Profile
	broken = true
	if err = m.BuildIndex(ctx, app.ID); err == nil {
		t.Fatal("failed embedding build succeeded")
	}
	_, current, _, err = m.App(ctx, app.ID)
	if err != nil || current.ActiveIndex.Profile != active {
		t.Fatal("failed build replaced live index", err)
	}
	found, err := m.Search(ctx, app.ID, "配置登录")
	if err != nil || len(found) != 1 {
		t.Fatal("keyword fallback failed", err)
	}
}

func TestCustomerStreamingAndCancellation(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event any) {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		send(map[string]any{"type": "response.output_text.delta", "delta": "这是一段正在生成的内容"})
		first <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		send(map[string]any{"type": "response.output_text.delta", "delta": "迟到内容"})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_mock", "status": "completed", "model": "mock-model", "output": []any{map[string]any{"type": "message", "id": "msg_mock", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "这是一段正在生成的内容迟到内容", "annotations": []any{}}}}}}})
	}))
	defer model.Close()
	app := models.SupportApp{Name: "Streaming", Config: customer.Raw(customer.Config{AIEnabled: true, Model: "mock-model", BaseURL: model.URL})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{APIKey: "mock-key"}); err != nil {
		t.Fatal(err)
	}
	session, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := m.Send(ctx, conv.ID, session, 0, customer.MessageInput{RequestID: rand.Text(), Content: "你好", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	var run models.AIRun
	if err = a.DB.Where("message_id = ? AND parent_id = 0", message.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.ProcessRun(ctx, run.ID) }()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("model not called")
	}
	deadline := time.Now().Add(3 * time.Second)
	snapshot := ""
	for time.Now().Before(deadline) {
		snapshot, _ = m.Redis.Get(ctx, m.Key(constants.TempAIStream, run.ReplyID)).Result()
		if strings.Contains(snapshot, "正在生成") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(snapshot, "正在生成") {
		t.Fatal("missing intermediate stream snapshot")
	}
	if _, err = m.Reception(ctx, conv.ID, session, 0, 0, 0, "human"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled run did not stop")
	}
	var reply models.ChatMessage
	if err = a.DB.First(&reply, run.ReplyID).Error; err != nil {
		t.Fatal(err)
	}
	if reply.Status != "cancelled" || strings.Contains(reply.Content, "迟到内容") {
		t.Fatal("late reply was committed", reply.Content, reply.Status)
	}
}

func TestCustomerSourceReimport(t *testing.T) {
	a := customerRuntime(t)
	ctx := context.Background()
	app := models.SupportApp{Name: "Import"}
	if err := a.Customer.CreateApp(ctx, &app, customer.Secrets{}); err != nil {
		t.Fatal(err)
	}
	source := models.KnowledgeSource{AppID: app.ID, Name: "Document", Kind: "text", Content: "Old published answer"}
	if err := a.DB.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Customer.ImportSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	var original models.KnowledgeEntry
	if err := a.DB.Where("source_id = ?", source.ID).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&original).Update("status", "published").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Customer.ImportSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.First(&original, original.ID).Error; err != nil || original.Status != "published" {
		t.Fatal("unchanged document was unpublished", err)
	}
	if err := a.DB.Model(&source).Update("content", "New answer awaiting review").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Customer.ImportSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	var entries []models.KnowledgeEntry
	if err := a.DB.Where("source_id = ?", source.ID).Order("id").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Status != "disabled" || entries[0].Version != 2 || entries[1].Status != "draft" {
		t.Fatal("old and new knowledge remained active together", entries)
	}
	if err := a.DB.Model(&entries[1]).Update("status", "published").Error; err != nil {
		t.Fatal(err)
	}
	if evidence, err := a.Customer.Search(ctx, app.ID, "Document"); err != nil || len(evidence) != 1 {
		t.Fatal("published source not searchable", evidence, err)
	}
	if err := a.DB.Delete(&source).Error; err != nil {
		t.Fatal(err)
	}
	if evidence, err := a.Customer.Search(ctx, app.ID, "Document"); err != nil || len(evidence) != 0 {
		t.Fatal("deleted source still searchable", evidence, err)
	}

}

func TestCustomerBatchUsers(t *testing.T) {
	a := customerRuntime(t)
	users := []models.User{{Nickname: "First"}, {Nickname: "Second"}}
	if err := a.DB.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	codec, err := identity.Load(context.Background(), a.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		var stored models.User
		if err := a.DB.First(&stored, user.ID).Error; err != nil {
			t.Fatal(err)
		}
		id, err := codec.Big2ID(stored.UniqueID)
		if err != nil || id != int64(user.ID) || stored.UniqueID != user.UniqueID {
			t.Fatal("batch public ID was not finalized", stored.UniqueID, err)
		}
	}
}

func TestCustomerWorkerInterruption(t *testing.T) {
	a := customerRuntime(t)
	m := a.Customer
	ctx := context.Background()
	started := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"部分内容\"}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer model.Close()
	app := models.SupportApp{Name: "Interrupted", Config: customer.Raw(customer.Config{AIEnabled: true, Model: "test", BaseURL: model.URL})}
	if err := m.CreateApp(ctx, &app, customer.Secrets{APIKey: "test"}); err != nil {
		t.Fatal(err)
	}
	visitor, conv, err := m.Open(ctx, app.PublicKey, "", customer.OpenInput{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := m.Send(ctx, conv.ID, visitor, 0, customer.MessageInput{RequestID: rand.Text(), Content: "question"})
	if err != nil {
		t.Fatal(err)
	}
	var run models.AIRun
	if err = a.DB.Where("message_id = ?", message.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	worker, stop := context.WithCancelCause(ctx)
	done := make(chan error, 1)
	go func() { done <- m.ProcessRun(worker, run.ID) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	stop(errors.New("received signal terminated"))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	if err = a.DB.First(&run, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "queued" || !strings.Contains(run.Error, "等待重新执行") {
		t.Fatal("worker shutdown lost the reply", run.Status, run.Error)
	}
}
