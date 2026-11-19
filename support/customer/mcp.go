package customer

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connectorTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t connectorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	for key, value := range t.headers {
		r.Header.Set(key, value)
	}
	return t.base.RoundTrip(r)
}

func (m *Manager) callMCP(ctx context.Context, actor ToolActor, tool Tool, args map[string]any) (any, error) {
	_, _, secrets, err := m.App(ctx, actor.AppID)
	if err != nil {
		return nil, err
	}
	client := *m.Client
	client.Transport = connectorTransport{base: http.DefaultTransport, headers: secrets.ToolHeaders[tool.Name]}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	implementation := mcp.NewClient(&mcp.Implementation{Name: "dawn-support", Version: "1.0"}, nil)
	session, err := implementation.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: tool.URL, HTTPClient: &client}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	// 远端工具收到由服务器构造的身份，调用方参数不能覆盖。
	return session.CallTool(ctx, &mcp.CallToolParams{Name: tool.RemoteName, Arguments: args, Meta: mcp.Meta{"dawn/actor": actor}})
}
