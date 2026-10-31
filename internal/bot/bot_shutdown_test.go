package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tb "gopkg.in/telebot.v4"
)

func TestWebhookPollerStopDoesNotCloseParentChannel(t *testing.T) {
	webhook := &tb.Webhook{}
	poller := webhookPoller{webhook: webhook}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer server.Close()
	b, err := tb.NewBot(tb.Settings{Token: "1:test", URL: server.URL, Poller: poller})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		poller.Poll(b, b.Updates, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not stop")
	}
}
