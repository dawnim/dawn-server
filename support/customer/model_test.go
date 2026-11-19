package customer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelProtocols(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				effort := body["reasoning_effort"]
				if protocol == "responses" {
					effort = body["reasoning"].(map[string]any)["effort"]
				}
				if protocol == "anthropic" {
					effort = body["output_config"].(map[string]any)["effort"]
					if r.Header.Get("x-api-key") != "test-key" {
						t.Error("missing anthropic auth")
					}
				}
				if effort != "high" {
					t.Error("reasoning effort", effort)
				}
				raw := string(Raw(body))
				if count == 2 && !strings.Contains(raw, "tool result") {
					t.Error("tool result lost", raw)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case "responses":
					if r.URL.Path != "/v1/responses" {
						t.Error(r.URL.Path)
					}
					if count == 1 {
						fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc1\",\"call_id\":\"call1\",\"name\":\"lookup\",\"arguments\":\"{}\",\"status\":\"completed\"}]}}\n\n")
					} else {
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"正文\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r2\",\"output\":[]}}\n\n")
					}
				case "chat_completions":
					if r.URL.Path != "/v1/chat/completions" {
						t.Error(r.URL.Path)
					}
					if count == 1 {
						fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"id\":\"c2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"正文\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				case "anthropic":
					if r.URL.Path != "/v1/messages" {
						t.Error(r.URL.Path)
					}
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"a1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
					if count == 1 {
						fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call1\",\"name\":\"lookup\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
					} else {
						fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"正文\"}}\n\n")
					}
					reason := "end_turn"
					if count == 1 {
						reason = "tool_use"
					}
					fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", reason)
				}
			}))
			defer server.Close()
			cfg := Config{ModelProtocol: protocol, Model: "test", BaseURL: server.URL + "/v1/", ReasoningEffort: "high"}
			m := &Manager{}
			input := []modelMessage{{Role: "user", Content: "question"}}
			text := ""
			first, err := m.modelTurn(context.Background(), cfg, "test-key", "instructions", input, []Tool{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, func(delta string) error { text += delta; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Calls) != 1 || first.Calls[0].Name != "lookup" || first.Calls[0].Arguments != "{}" {
				t.Fatalf("tool call: %+v", first)
			}
			if protocol == "responses" {
				var items []map[string]any
				if err := json.Unmarshal(first.Raw, &items); err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					if _, exists := item["content"]; exists {
						t.Fatal("tool call contains fields from another response variant", item)
					}
				}
			}
			input = append(input, first, modelMessage{Role: "tool", CallID: first.Calls[0].CallID, Content: "tool result"})
			result, err := m.modelTurn(context.Background(), cfg, "test-key", "instructions", input, nil, func(delta string) error { text += delta; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if result.Content != "正文" || text != "正文" {
				t.Fatal(result.Content, text)
			}
		})
	}
}

func TestModelFailureDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "request-123")
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":"unsupported_parameter","message":"unsupported test-key option"}}`)
	}))
	defer server.Close()
	m := &Manager{}
	_, err := m.modelTurn(context.Background(), Config{ModelProtocol: "responses", Model: "test", BaseURL: server.URL}, "test-key", "", []modelMessage{{Role: "user", Content: "hello"}}, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported_parameter") || !strings.Contains(err.Error(), "request-123") || strings.Contains(err.Error(), "test-key") {
		t.Fatal(err)
	}
}

func TestModelIncompleteAndDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["max_output_tokens"]; ok {
			t.Error("default output limit must be omitted")
		}
		if _, ok := body["reasoning"]; ok {
			t.Error("default reasoning must be omitted")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r1\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	}))
	defer server.Close()
	m := &Manager{}
	_, err := m.modelTurn(context.Background(), Config{ModelProtocol: "responses", Model: "test", BaseURL: server.URL}, "key", "", []modelMessage{{Role: "user", Content: "hello"}}, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatal(err)
	}
}

func TestModelCompletedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"正文\"}]}]}}\n\n")
	}))
	defer server.Close()
	m := &Manager{}
	streamed := ""
	result, err := m.modelTurn(context.Background(), Config{ModelProtocol: "responses", Model: "test", BaseURL: server.URL}, "key", "", []modelMessage{{Role: "user", Content: "hello"}}, nil, func(delta string) error { streamed += delta; return nil })
	if err != nil || result.Content != "正文" || streamed != "正文" {
		t.Fatal(result.Content, streamed, err)
	}
}
