package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	aoption "github.com/anthropics/anthropic-sdk-go/option"
	aparam "github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type modelCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type modelMessage struct {
	Role    string          `json:"role"`
	Content string          `json:"content"`
	CallID  string          `json:"call_id,omitempty"`
	Calls   []modelCall     `json:"calls,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

// modelTurn只适配协议，知识、工具授权、接待版本和推送仍由同一执行器处理。
func (m *Manager) modelTurn(ctx context.Context, cfg Config, key, instructions string, input []modelMessage, tools []Tool, text func(string) error) (result modelMessage, err error) {
	result.Role = "assistant"
	var httpResponse *http.Response
	defer func() {
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		message := err.Error()
		var oe *openai.Error
		var ae *anthropic.Error
		if errors.As(err, &oe) {
			message = fmt.Sprintf("HTTP %d: %s (%s)", oe.StatusCode, oe.Message, oe.Code)
			if oe.Response != nil {
				message += ", request_id=" + oe.Response.Header.Get("x-request-id")
			}
		}
		if errors.As(err, &ae) {
			var body struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(ae.RawJSON()), &body)
			message = fmt.Sprintf("HTTP %d: %s (%s), request_id=%s", ae.StatusCode, body.Error.Message, body.Error.Type, ae.RequestID)
		}
		if httpResponse != nil {
			message += ", endpoint=" + httpResponse.Request.URL.EscapedPath()
			if oe == nil && ae == nil {
				message += ", request_id=" + httpResponse.Header.Get("x-request-id")
			}
		}
		message = strings.ReplaceAll(message, key, "[redacted]")
		message = strings.SplitN(message, "Request:", 2)[0]
		if len(message) > 1500 {
			message = message[:1500]
		}
		err = fmt.Errorf("%s: %s", cfg.ModelProtocol, message)
	}()
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL != "" {
		u, e := url.Parse(baseURL)
		if e != nil {
			return result, e
		}
		if u.Path == "" || u.Path == "/" {
			baseURL += "/v1"
		}
	}
	if cfg.ModelProtocol == "anthropic" {
		opts := []aoption.RequestOption{aoption.WithAPIKey(key), aoption.WithMaxRetries(0), aoption.WithResponseInto(&httpResponse)}
		if cfg.BaseURL != "" {
			opts = append(opts, aoption.WithBaseURL(strings.TrimSuffix(baseURL, "/v1")+"/"))
		}
		client := anthropic.NewClient(opts...)
		params := anthropic.MessageNewParams{Model: anthropic.Model(cfg.Model), MaxTokens: 8192, System: []anthropic.TextBlockParam{{Text: instructions}}}
		if cfg.MaxOutputTokens > 0 {
			params.MaxTokens = cfg.MaxOutputTokens
		}
		if cfg.ReasoningEffort == "none" {
			params.Thinking.OfDisabled = &anthropic.ThinkingConfigDisabledParam{}
		} else if cfg.ReasoningEffort != "" {
			params.Thinking.OfAdaptive = &anthropic.ThinkingConfigAdaptiveParam{}
			params.OutputConfig.Effort = anthropic.OutputConfigEffort(cfg.ReasoningEffort)
		}
		for _, item := range input {
			msg := anthropic.MessageParam{Role: anthropic.MessageParamRole(item.Role), Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(item.Content)}}
			if item.Role == "tool" {
				msg = anthropic.NewUserMessage(anthropic.NewToolResultBlock(item.CallID, item.Content, false))
			}
			if len(item.Raw) > 0 {
				msg = aparam.Override[anthropic.MessageParam](item.Raw)
			}
			// 同一轮的多个工具结果放在一条user消息中，保留完整tool_use对应关系。
			if item.Role == "tool" && len(params.Messages) > 0 && params.Messages[len(params.Messages)-1].Role == "user" && len(params.Messages[len(params.Messages)-1].Content) > 0 && params.Messages[len(params.Messages)-1].Content[0].OfToolResult != nil {
				params.Messages[len(params.Messages)-1].Content = append(params.Messages[len(params.Messages)-1].Content, msg.Content...)
			} else {
				params.Messages = append(params.Messages, msg)
			}
		}
		for _, t := range tools {
			params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: aparam.Override[anthropic.ToolInputSchemaParam](Raw(t.Parameters))}})
		}
		stream := client.Messages.NewStreaming(ctx, params)
		defer stream.Close()
		var message anthropic.Message
		stopped := false
		for stream.Next() {
			event := stream.Current()
			if err = message.Accumulate(event); err != nil {
				return
			}
			if event.Type == "message_stop" {
				stopped = true
			}
			if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
				result.Content += event.Delta.Text
				if err = text(event.Delta.Text); err != nil {
					return
				}
			}
		}
		if err = stream.Err(); err != nil {
			return
		}
		if !stopped {
			err = errors.New("模型流未完整结束")
			return
		}
		if message.StopReason == "max_tokens" {
			err = errors.New("模型达到输出上限，请提高最大输出Token数或降低推理强度")
			return
		}
		result.Raw = Raw(message.ToParam())
		for _, block := range message.Content {
			if block.Type == "tool_use" {
				result.Calls = append(result.Calls, modelCall{block.ID, block.Name, string(block.Input)})
			}
		}
		return
	}
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0), option.WithResponseInto(&httpResponse)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL+"/"))
	}
	client := openai.NewClient(opts...)
	if cfg.ModelProtocol == "chat_completions" {
		params := openai.ChatCompletionNewParams{Model: shared.ChatModel(cfg.Model), Messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(instructions)}}
		if cfg.MaxOutputTokens > 0 {
			params.MaxCompletionTokens = openai.Int(cfg.MaxOutputTokens)
		}
		if cfg.ReasoningEffort != "" {
			params.ReasoningEffort = shared.ReasoningEffort(cfg.ReasoningEffort)
		}
		for _, item := range input {
			switch {
			case len(item.Raw) > 0:
				params.Messages = append(params.Messages, param.Override[openai.ChatCompletionMessageParamUnion](item.Raw))
			case item.Role == "tool":
				params.Messages = append(params.Messages, openai.ToolMessage(item.Content, item.CallID))
			case item.Role == "assistant":
				params.Messages = append(params.Messages, openai.AssistantMessage(item.Content))
			default:
				params.Messages = append(params.Messages, openai.UserMessage(item.Content))
			}
		}
		for _, t := range tools {
			params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: t.Name, Description: openai.String(t.Description), Parameters: shared.FunctionParameters(t.Parameters)}))
		}
		stream := client.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()
		accumulator := openai.ChatCompletionAccumulator{}
		for stream.Next() {
			chunk := stream.Current()
			accumulator.AddChunk(chunk)
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				delta := chunk.Choices[0].Delta.Content
				result.Content += delta
				if err = text(delta); err != nil {
					return
				}
			}
		}
		if err = stream.Err(); err != nil {
			return
		}
		if len(accumulator.Choices) == 0 || accumulator.Choices[0].FinishReason == "" {
			err = errors.New("模型流未完整结束")
			return
		}
		if accumulator.Choices[0].FinishReason == "length" {
			err = errors.New("模型达到输出上限，请提高最大输出Token数或降低推理强度")
			return
		}
		message := accumulator.Choices[0].Message
		result.Raw = Raw(message.ToParam())
		for _, call := range message.ToolCalls {
			result.Calls = append(result.Calls, modelCall{call.ID, call.Function.Name, call.Function.Arguments})
		}
		return
	}
	params := responses.ResponseNewParams{Model: shared.ResponsesModel(cfg.Model), Instructions: openai.String(instructions), Store: openai.Bool(false), Include: []responses.ResponseIncludable{"reasoning.encrypted_content"}}
	if cfg.MaxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(cfg.MaxOutputTokens)
	}
	if cfg.ReasoningEffort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(cfg.ReasoningEffort)}
	}
	list := responses.ResponseInputParam{}
	for _, item := range input {
		switch {
		case len(item.Raw) > 0:
			var raw []json.RawMessage
			if err = json.Unmarshal(item.Raw, &raw); err != nil {
				return
			}
			for _, value := range raw {
				list = append(list, param.Override[responses.ResponseInputItemUnionParam](value))
			}
		case item.Role == "tool":
			list = append(list, responses.ResponseInputItemParamOfFunctionCallOutput(item.CallID, item.Content))
		default:
			list = append(list, responses.ResponseInputItemParamOfMessage(item.Content, responses.EasyInputMessageRole(item.Role)))
		}
	}
	params.Input.OfInputItemList = list
	for _, t := range tools {
		params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{Name: t.Name, Description: openai.String(t.Description), Parameters: t.Parameters, Strict: openai.Bool(false)}})
	}
	stream := client.Responses.NewStreaming(ctx, params)
	defer stream.Close()
	var response responses.Response
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "response.output_text.delta":
			result.Content += event.Delta
			if err = text(event.Delta); err != nil {
				return
			}
		case "response.completed":
			response = event.Response
		case "response.failed":
			err = fmt.Errorf("%s: %s", event.Response.Error.Code, event.Response.Error.Message)
			return
		case "error":
			err = fmt.Errorf("%s: %s", event.Code, event.Message)
			return
		case "response.incomplete":
			err = fmt.Errorf("模型响应未完成: %s，请检查输出上限与推理强度", event.Response.IncompleteDetails.Reason)
			return
		}
	}
	if err = stream.Err(); err != nil {
		return
	}
	if response.ID == "" {
		err = errors.New("模型流未完整结束")
		return
	}
	if result.Content == "" {
		result.Content = response.OutputText()
		if result.Content != "" {
			if err = text(result.Content); err != nil {
				return
			}
		}
	}
	// 响应联合类型包含其他变体字段，只重放服务实际返回的JSON。
	items := make([]json.RawMessage, 0, len(response.Output))
	for _, item := range response.Output {
		items = append(items, json.RawMessage(item.RawJSON()))
	}
	result.Raw = Raw(items)
	for _, item := range response.Output {
		if item.Type == "function_call" {
			call := item.AsFunctionCall()
			result.Calls = append(result.Calls, modelCall{call.CallID, call.Name, call.Arguments})
		}
	}
	return
}

// TestModel使用已保存配置验证真实流式输出，错误仅返回给应用配置管理员。
func (m *Manager) TestModel(ctx context.Context, appID uint) (map[string]any, error) {
	_, cfg, secrets, err := m.App(ctx, appID)
	if err != nil {
		return nil, err
	}
	return m.CheckModel(ctx, cfg, secrets.APIKey)
}

// CheckModel 验证待保存配置，失败时调用方不得写入新密钥。
func (m *Manager) CheckModel(ctx context.Context, cfg Config, key string) (map[string]any, error) {
	if cfg.Model == "" || key == "" {
		return nil, ErrInvalid
	}
	input := []modelMessage{{Role: "user", Content: "请先调用support_ping，然后只回复OK"}}
	tools := []Tool{{Name: "support_ping", Description: "检查工具调用连通性", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
	for step := 0; step < 3; step++ {
		result, e := m.modelTurn(ctx, cfg, key, "完成连通性检查", input, tools, func(string) error { return nil })
		if e != nil {
			return nil, e
		}
		if len(result.Calls) == 0 {
			if strings.TrimSpace(result.Content) == "" {
				return nil, errors.New("模型未返回文本")
			}
			return map[string]any{"model_protocol": cfg.ModelProtocol, "model": cfg.Model, "content": result.Content, "tool_tested": step > 0}, nil
		}
		input = append(input, result)
		for _, call := range result.Calls {
			input = append(input, modelMessage{Role: "tool", CallID: call.CallID, Content: `{"ok":true}`})
		}
	}
	return nil, errors.New("模型未完成工具调用检查")
}

// GenerateText uses the configured customer model for application text generation.
func (m *Manager) GenerateText(ctx context.Context, appID uint, instructions, input string) (string, error) {
	_, cfg, secrets, err := m.App(ctx, appID)
	if err != nil {
		return "", err
	}
	result, err := m.modelTurn(ctx, cfg, secrets.APIKey, instructions, []modelMessage{{Role: "user", Content: input}}, nil, func(string) error { return nil })
	return result.Content, err
}

// GenerateKnowledge 将管理员提供的资料整理为可编辑知识，生成过程不发布内容。
func (m *Manager) GenerateKnowledge(ctx context.Context, appID uint, content string) ([]map[string]any, error) {
	result, err := m.GenerateText(ctx, appID, `将资料整理为知识条目，只返回JSON数组，不加代码围栏。每项包含question、answer、keywords、conditions、locale、location，所有字段均为字符串。答案使用Markdown；按独立主题拆分，标题避免重复；仅依据原文，保留适用条件和来源，不编造。`, content)
	if err != nil {
		return nil, err
	}
	var entries []map[string]any
	if err = json.Unmarshal([]byte(result), &entries); err != nil {
		return nil, ErrInvalid
	}
	if len(entries) == 0 {
		return nil, ErrInvalid
	}
	for _, entry := range entries {
		for key, value := range entry {
			switch key {
			case "question", "answer", "keywords", "conditions", "locale", "location":
				if _, ok := value.(string); !ok {
					return nil, ErrInvalid
				}
			default:
				delete(entry, key)
			}
		}
		question, _ := entry["question"].(string)
		answer, _ := entry["answer"].(string)
		if strings.TrimSpace(question) == "" || strings.TrimSpace(answer) == "" {
			return nil, ErrInvalid
		}
	}
	return entries, nil
}
