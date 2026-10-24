package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/expensewire"
	"github.com/LionJr/expenses/internal/httpapi"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/internal/telegram"
)

func checkStaffFixes(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	create := func(t *testing.T, comment string) expense.Expense {
		t.Helper()
		e, err := expensesvc.New(s).Create(ctx, expense.Input{UserID: 101, Amount: 10000, Category: "жильё", SpentAt: "2045-10-19", Comment: comment})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := expensesvc.New(s).Delete(ctx, 0, e.ID); err != nil && !errors.Is(err, expense.ErrNotFound) {
				t.Error(err)
			}
		})
		return e
	}
	handler := (&httpapi.API{Store: expensesvc.New(s), RequestTimeout: 5 * time.Second, Timezone: "Asia/Ashgabat"}).Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	t.Run("optimistic editing preserves concurrent amount", func(t *testing.T) {
		e := create(t, "version original")
		if e.Version != 1 {
			t.Fatal("new record must start at version 1")
		}
		path := fmt.Sprintf("/api/expenses/%d", e.ID)
		first := request(http.MethodPatch, path, fmt.Sprintf(`{"amount":"20000","expected_version":"%d"}`, e.Version))
		if first.Code != http.StatusOK {
			t.Fatal(first.Code, first.Body)
		}
		var changed expensewire.Expense
		if err := json.Unmarshal(first.Body.Bytes(), &changed); err != nil {
			t.Fatal(err)
		}
		if changed.Version != 2 {
			t.Fatal("version did not increment")
		}
		// A stale full form is rejected even if it resends the previous amount.
		stale := request(http.MethodPatch, path, fmt.Sprintf(`{"amount":"10000","category":"жильё","spent_at":"2045-10-19","comment":"new comment","expected_version":"%d"}`, e.Version))
		if stale.Code != http.StatusConflict {
			t.Fatal(stale.Code, stale.Body)
		}
		page, err := expensesvc.New(s).List(ctx, expense.Filter{ID: e.ID, UserID: 101})
		if err != nil || page.Items[0].Amount != 20000 || page.Items[0].Comment != "version original" || page.Items[0].Version != 2 {
			t.Fatalf("stale form changed record: %+v %v", page, err)
		}
		fresh := request(http.MethodPatch, path, `{"comment":"","expected_version":"2"}`)
		if fresh.Code != http.StatusOK {
			t.Fatal(fresh.Code, fresh.Body)
		}
		var wire map[string]any
		_ = json.Unmarshal(fresh.Body.Bytes(), &wire)
		if wire["version"] != "3" || wire["amount"] != "20000" || wire["comment"] != "" {
			t.Fatal(wire)
		}
		if w := request(http.MethodPatch, path, `{"expected_version":"3"}`); w.Code != http.StatusBadRequest {
			t.Fatal("version-only patch accepted")
		}
		if w := request(http.MethodPatch, path, `{"comment":"x","expected_version":"0"}`); w.Code != http.StatusBadRequest {
			t.Fatal("invalid version accepted")
		}
		if _, err := expensesvc.New(s).Update(ctx, 202, e.ID, expense.Patch{Comment: &e.Comment, ExpectedVersion: &e.Version}); !errors.Is(err, expense.ErrNotFound) {
			t.Fatal("foreign version check disclosed a row", err)
		}
	})
	t.Run("only one concurrent writer can use a version", func(t *testing.T) {
		e := create(t, "concurrent version")
		path := fmt.Sprintf("/api/expenses/%d", e.ID)
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for _, amount := range []string{"20000", "30000"} {
			wg.Add(1)
			go func(amount string) {
				defer wg.Done()
				codes <- request(http.MethodPatch, path, fmt.Sprintf(`{"amount":"%s","expected_version":"1"}`, amount)).Code
			}(amount)
		}
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if counts[200] != 1 || counts[409] != 1 {
			t.Fatal(counts)
		}
		page, err := expensesvc.New(s).List(ctx, expense.Filter{ID: e.ID, UserID: 101})
		if err != nil || page.Items[0].Version != 2 {
			t.Fatal("wrong version after concurrent updates")
		}
	})
	t.Run("invalid text is a controlled input error", func(t *testing.T) {
		w := request(http.MethodPost, "/api/expenses", `{"user_id":"101","amount":"100","category":"еда","spent_at":"2045-10-19","comment":"bad\u0000comment"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatal(w.Code, w.Body)
		}
		e := create(t, "valid text")
		if w := request(http.MethodPatch, fmt.Sprintf("/api/expenses/%d", e.ID), `{"comment":"bad\u0000comment"}`); w.Code != http.StatusBadRequest {
			t.Fatal(w.Code, w.Body)
		}
		if _, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, Search: "bad\x00search"}); !errors.Is(err, expense.ErrInvalid) {
			t.Fatal("invalid search accepted", err)
		}
		if err := s.SaveSession(ctx, 101, json.RawMessage(`{"request":{"text":"bad\u0000text"}}`)); !errors.Is(err, expense.ErrInvalid) {
			t.Fatal("invalid session accepted", err)
		}
	})
	t.Run("Russian aliases update and delete existing expense", func(t *testing.T) {
		e := create(t, "аренда квартиры morphology-fixed")
		provider := &offlineProvider{}
		a := &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second}
		reply, _, err := a.Reply(ctx, 101, 19001, "измени аренду на 12000", agent.Session{})
		if err != nil || !strings.Contains(reply, "Выполнено") {
			t.Fatal(reply, err)
		}
		page, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, ID: e.ID})
		if err != nil || page.Items[0].Amount != 1200000 {
			t.Fatal("alias update failed", page, err)
		}
		reply, _, err = a.Reply(ctx, 101, 19002, "удали аренду", agent.Session{})
		if err != nil || !strings.Contains(reply, "расход удалён") || provider.calls != 0 {
			t.Fatal(reply, err)
		}
	})
	t.Run("permanent bad argument does not block next participant", func(t *testing.T) {
		provider := &scriptedProvider{responses: []agent.Message{toolMessage("create_expense", `{"amount_decimal":"1","category":"еда","spent_at":"2045-10-19","comment":"bad\u0000comment"}`), toolMessage("clarify_request", `{"field":"request"}`)}}
		first := privateUpdate(t, 19101, 19101, "кофе 1")
		second := privateUpdate(t, 19102, 19102, "/start")
		second.Message.From.ID, second.Message.Chat.ID = 202, 202
		polls, sends := 0, 0
		var cancel context.CancelFunc
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/getUpdates") {
				polls++
				if polls == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []telegram.Update{first, second}})
					return
				}
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
				cancel()
				return
			}
			sends++
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":20000}}`)
		}))
		defer server.Close()
		bot := &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: postgres.AllowedForTest(s), Store: s, Agent: &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second}}
		runCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		cancel = stop
		defer stop()
		if err := bot.Run(runCtx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		for _, id := range []int64{19101, 19102} {
			user := int64(101)
			if id == 19102 {
				user = 202
			}
			state, _, err := s.StartUpdate(ctx, id, user)
			if err != nil || state != "sent" {
				t.Fatalf("update %d blocked queue: %s %v", id, state, err)
			}
		}
		if sends != 2 {
			t.Fatal("next participant did not receive reply", sends)
		}
		calls := provider.calls
		if err := bot.Handle(ctx, first); err != nil {
			t.Fatal(err)
		}
		if provider.calls != calls {
			t.Fatal("permanent rejection reprocessed")
		}
	})
	t.Run("create clarification is persisted between messages", func(t *testing.T) {
		provider := &scriptedProvider{responses: []agent.Message{toolMessage("clarify_request", `{"field":"amount"}`), toolMessage("create_expense", `{"amount_decimal":"350.50","category":"еда","spent_at":"2045-10-19","comment":"create-draft-fixed"}`)}}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":22000}}`)
		}))
		defer server.Close()
		bot := &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: postgres.AllowedForTest(s), Store: s, Agent: &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second}}
		if err := bot.Handle(ctx, privateUpdate(t, 19201, 19201, "кофе")); err != nil {
			t.Fatal(err)
		}
		if loadChoiceSession(t, s).Request == nil {
			t.Fatal("draft missing from PostgreSQL")
		}
		// Replace all process-local bot/agent state; only PostgreSQL carries the draft.
		bot.Agent = &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second}
		if err := bot.Handle(ctx, privateUpdate(t, 19202, 19202, "350,50")); err != nil {
			t.Fatal(err)
		}
		page, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, Search: "create-draft-fixed"})
		if err != nil || page.Total != 1 || page.Items[0].Amount != 35050 || loadChoiceSession(t, s).Request != nil {
			t.Fatal("draft creation failed", page, err)
		}
		if err := expensesvc.New(s).Delete(ctx, 0, page.Items[0].ID); err != nil {
			t.Fatal(err)
		}
	})
}
