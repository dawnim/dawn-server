package customer

import (
	"github.com/dawnim/dawn-server/app/models"
	"testing"
	"time"
)

func TestOfflineDeadline(t *testing.T) {
	since := time.Now().UnixMilli()
	for _, test := range []struct {
		name             string
		minutes, closing *int
		status           string
		requested        int64
		want             int64
	}{
		{name: "default", status: "active", want: since + 10*time.Minute.Milliseconds()},
		{name: "closing default", status: "closing_pending", requested: since, want: since + 3*time.Minute.Milliseconds()},
		{name: "disabled", minutes: new(int), status: "active"},
		{name: "closing disabled with reception", minutes: new(int), status: "closing_pending"},
		{name: "closing inherits", closing: new(int), status: "closing_pending", want: since + 10*time.Minute.Milliseconds()},
		{name: "closed", status: "closed"},
		{name: "cancelled queue", status: "cancelled"},
		{name: "request after disconnect", status: "closing_pending", requested: since + 2*time.Minute.Milliseconds(), want: since + 5*time.Minute.Milliseconds()},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{OfflineTimeoutMinutes: test.minutes, ClosingOfflineMinutes: test.closing}
			row := models.SupportSession{Status: test.status, OfflineSince: since, ClosingRequestedAt: test.requested}
			if got := cfg.OfflineDeadline(&row); got != test.want {
				t.Fatalf("got %d want %d", got, test.want)
			}
		})
	}
	if (Config{}).OfflineDeadline(&models.SupportSession{Status: "active"}) != 0 {
		t.Fatal("online session got deadline")
	}
}
