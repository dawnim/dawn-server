package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
)

type request struct {
	Selection string `json:"selection"`
	Action    string `json:"action"`
}

type Response struct {
	PID      int      `json:"pid"`
	Services []Status `json:"services"`
	Error    string   `json:"error,omitempty"`
}

// Control 向已经运行的本地管理器提交操作并等待结果。
func Control(ctx context.Context, socket, selection, action string) (Response, bool, error) {
	var result Response
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return result, !socketAbsent(err), err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := json.NewEncoder(conn).Encode(request{Selection: selection, Action: action}); err != nil {
		return result, true, err
	}
	if err := json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&result); err != nil {
		return result, true, err
	}
	if result.Error != "" {
		return result, true, errors.New(result.Error)
	}
	return result, true, nil
}

// Parse 供 CLI 和本地控制通道共用同一套服务选择规则。
func Parse(selection, action string) ([]string, error) {
	names := strings.Split(selection, ",")
	if selection == "all" || selection == "local" {
		names = []string{"http", "ws", "horizon", "cron"}
		if selection == "local" {
			names = append(names, "frontend")
		}
	}
	for i, name := range names {
		names[i] = strings.TrimSpace(name)
		if !slices.Contains([]string{"http", "ws", "horizon", "cron", "frontend"}, names[i]) {
			return nil, i18n.Errorf("未知服务: %s", name)
		}
		if action == "reload" && names[i] != "http" && names[i] != "ws" {
			return nil, i18n.Errorf("reload只支持http和ws，其他服务使用restart")
		}
	}
	if !slices.Contains([]string{"start", "stop", "restart", "reload", "status"}, action) {
		return nil, i18n.Errorf("未知服务操作: %s", action)
	}
	return slices.Compact(slices.Sorted(slices.Values(names))), nil
}
