package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelLinkIssuedOnlyToMasterWithoutBlockingOtherBotUsers(t *testing.T) {
	for _, id := range []int64{42, 99} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
			defer server.Close()
			store := &memoryStore{}
			llm := &responder{}
			links := 0
			b := &Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true, 99: true}, Store: store, Agent: llm, PanelMasterID: 42, PanelLink: func(ctx context.Context, user int64) (string, error) {
				links++
				if user != 42 {
					t.Error("non-master reached invitation service")
				}
				return "https://family.example/auth", nil
			}}
			u := update(t, fmt.Sprintf(`{"update_id":2,"message":{"from":{"id":%d},"chat":{"id":%d,"type":"private"},"text":"/panel"}}`, id, id))
			if err := b.Handle(t.Context(), u); err != nil {
				t.Fatal(err)
			}
			if llm.calls != 0 || store.state != "sent" {
				t.Fatal("panel command reached LLM or remained retryable")
			}
			if id == 42 {
				if links != 1 || !strings.Contains(store.reply, "https://family.example/auth") {
					t.Fatal("master did not receive link")
				}
			} else if links != 0 || !strings.Contains(store.reply, "только главному") || strings.Contains(store.reply, "https://") {
				t.Fatal("bot-only user received panel access")
			}
		})
	}
}
