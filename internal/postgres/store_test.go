package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/expensewire"
	"github.com/LionJr/expenses/internal/httpapi"
	"github.com/LionJr/expenses/internal/postgres"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL pointing to a dedicated database ending in _test")
	}
	parsed, parseErr := url.Parse(dsn)
	if parseErr != nil || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("TEST_DATABASE_URL must name a dedicated database ending in _test")
	}
	ctx := context.Background()
	s, e := postgres.Open(ctx, dsn, []int64{101, 202})
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	var dbName string
	if e = s.DB.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName); e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(dbName, "_test") {
		t.Fatal("refusing integration tests outside a dedicated _test database")
	}
	if _, e = s.DB.ExecContext(ctx, "TRUNCATE expense_operations,telegram_updates,bot_sessions,expenses RESTART IDENTITY"); e != nil {
		t.Fatal(e)
	}
	t.Run("delivery state enum", func(t *testing.T) {
		var stateType string
		if err := s.DB.QueryRowContext(ctx, `SELECT udt_name FROM information_schema.columns
            WHERE table_schema = current_schema() AND table_name = 'telegram_updates' AND column_name = 'state'`).Scan(&stateType); err != nil {
			t.Fatal(err)
		}
		if stateType != "telegram_update_state" {
			t.Fatalf("unexpected state type: %s", stateType)
		}
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO telegram_updates(update_id,user_id,state) VALUES(-1,101,'invalid')"); err == nil {
			t.Fatal("invalid delivery state accepted")
		}
	})
	create := func(user, amount int64, date, comment string) expense.Expense {
		t.Helper()
		v, e := s.Create(ctx, expense.Input{UserID: user, Amount: amount, Category: "еда", SpentAt: date, Comment: comment})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	first := create(101, 35000, "2026-10-17", "кофе")
	other := create(202, 50000, "2026-10-17", "обед")
	last := create(101, 120000, "2026-09-01", "такси")
	t.Run("owner isolation", func(t *testing.T) {
		own, err := s.List(ctx, expense.Filter{UserID: 101, ID: first.ID, Limit: 1})
		if err != nil || own.Total != 1 || len(own.Items) != 1 || own.Items[0].ID != first.ID {
			t.Fatalf("own ID lookup: %+v %v", own, err)
		}
		foreign, err := s.List(ctx, expense.Filter{UserID: 101, ID: other.ID, Limit: 1})
		if err != nil || foreign.Total != 0 || len(foreign.Items) != 0 || foreign.TotalAmount != 0 {
			t.Fatalf("foreign ID lookup: %+v %v", foreign, err)
		}
		p, e := s.List(ctx, expense.Filter{UserID: 101})
		if e != nil || p.Total != 2 || p.TotalAmount != 155000 {
			t.Fatalf("page: %+v %v", p, e)
		}
		n := int64(1)
		if _, e = s.Update(ctx, 101, other.ID, expense.Patch{Amount: &n}); !errors.Is(e, expense.ErrNotFound) {
			t.Fatalf("foreign update: %v", e)
		}
		if e = s.Delete(ctx, 101, other.ID); !errors.Is(e, expense.ErrNotFound) {
			t.Fatalf("foreign delete: %v", e)
		}
	})
	t.Run("filter pagination aggregate latest", func(t *testing.T) {
		p, e := s.List(ctx, expense.Filter{UserID: 101, Limit: 1})
		if e != nil || len(p.Items) != 1 || p.TotalAmount != 155000 {
			t.Fatalf("aggregate: %+v %v", p, e)
		}
		p, e = s.List(ctx, expense.Filter{UserID: 101, Limit: 1, Latest: true})
		if e != nil || p.Items[0].ID != last.ID {
			t.Fatalf("latest: %+v %v", p, e)
		}
		p, e = s.List(ctx, expense.Filter{From: "2026-10-17", To: "2026-10-17", Category: "еда", Search: "кофе"})
		if e != nil || p.TotalAmount != 35000 || p.Total != 1 {
			t.Fatalf("filters: %+v %v", p, e)
		}
		p, e = s.List(ctx, expense.Filter{Offset: 100})
		if e != nil || p.Total != 3 || len(p.Items) != 0 || p.TotalAmount != 205000 {
			t.Fatalf("empty page: %+v %v", p, e)
		}
	})
	t.Run("patch clear comment timestamps", func(t *testing.T) {
		empty := ""
		v, e := s.Update(ctx, 101, first.ID, expense.Patch{Comment: &empty})
		if e != nil || v.Comment != "" || v.Amount != 35000 || v.UpdatedAt.Before(first.UpdatedAt) {
			t.Fatalf("patch: %+v %v", v, e)
		}
	})
	t.Run("stale expected amount blocks concurrent update", func(t *testing.T) {
		stale, amount := int64(1), int64(999)
		if _, e := s.Update(ctx, 101, first.ID, expense.Patch{Amount: &amount, ExpectedAmount: &stale}); !errors.Is(e, expense.ErrNotFound) {
			t.Fatalf("stale update accepted: %v", e)
		}
	})
	t.Run("atomic concurrent operation replay", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan string, 6)
		errs := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, e := s.Execute(ctx, "replay", 101, expense.Command{Operation: expense.Create, Input: expense.Input{Amount: 123, Category: "другое", SpentAt: "2026-10-17"}})
				results <- string(v)
				errs <- e
			}()
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		var id string
		for v := range results {
			var r map[string]any
			if e = json.Unmarshal([]byte(v), &r); e != nil {
				t.Fatal(e)
			}
			got := r["id"].(string)
			if id != "" && id != got {
				t.Fatal("replay changed result")
			}
			id = got
		}
		// Recovery must win over new-command validation, including through the
		// shared service: the operation may already have committed before a crash.
		replayed, replayErr := expensesvc.New(s).Execute(ctx, "replay", 101, expense.Command{})
		var recovered map[string]any
		if replayErr != nil || json.Unmarshal(replayed, &recovered) != nil || recovered["id"] != id || recovered["operation"] != "create_expense" {
			t.Fatalf("committed operation was revalidated: %s %v", replayed, replayErr)
		}
		p, e := s.List(ctx, expense.Filter{UserID: 101})
		if e != nil || p.Total != 3 {
			t.Fatalf("duplicate creation: %+v %v", p, e)
		}
		if _, e = s.Execute(ctx, "invalid", 101, expense.Command{Operation: expense.Create, Input: expense.Input{Amount: -1}}); e == nil {
			t.Fatal("invalid operation committed")
		}
		var count int
		if e = s.DB.QueryRowContext(ctx, "SELECT count(*) FROM expense_operations WHERE operation_key='invalid'").Scan(&count); e != nil || count != 0 {
			t.Fatal("failed operation journaled")
		}
	})
	t.Run("sessions and update state", func(t *testing.T) {
		if e = s.SaveSession(ctx, 101, json.RawMessage(`{"history":[]}`)); e != nil {
			t.Fatal(e)
		}
		if b, e := s.LoadSession(ctx, 101); e != nil || len(b) == 0 {
			t.Fatal("missing session")
		}
		if _, e = s.DB.ExecContext(ctx, "UPDATE bot_sessions SET expires_at=now()-interval '1 minute' WHERE user_id=101"); e != nil {
			t.Fatal(e)
		}
		if b, e := s.LoadSession(ctx, 101); e != nil || len(b) != 0 {
			t.Fatal("expired session loaded")
		}
		state, _, e := s.StartUpdate(ctx, 9, 101)
		if e != nil || state != "processing" {
			t.Fatal(e)
		}
		if e = s.FinishUpdate(ctx, 9, "готово", json.RawMessage(`{}`), 101); e != nil {
			t.Fatal(e)
		}
		state, reply, e := s.StartUpdate(ctx, 9, 101)
		if e != nil || state != "done" || reply != "готово" {
			t.Fatal("update state lost")
		}
		if e = s.MarkSent(ctx, 9); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("REST contract", func(t *testing.T) {
		handler := (&httpapi.API{Store: expensesvc.New(s), Timezone: "Europe/Moscow"}).Handler()
		request := func(method, path, body string) *httptest.ResponseRecorder {
			t.Helper()
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		w := request("POST", "/api/expenses", `{"user_id":"101","amount":"35050","category":"еда","spent_at":"2026-10-17","comment":"API"}`)
		if w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body)
		}
		var created expensewire.Expense
		if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
			t.Fatal(e)
		}
		path := fmt.Sprintf("/api/expenses/%d", created.ID)
		if w = request("PATCH", path, `{"comment":"","amount":"9223372036854775807"}`); w.Code != 200 {
			t.Fatalf("patch %d %s", w.Code, w.Body)
		}
		if w = request("GET", "/api/expenses?user_id=101", ""); w.Code != 400 {
			t.Fatalf("overflow not handled: %d", w.Code)
		}
		if w = request("DELETE", path, ""); w.Code != 204 {
			t.Fatal(w.Code)
		}
		if w = request("DELETE", path, ""); w.Code != 404 {
			t.Fatal(w.Code)
		}
		for _, body := range []string{`{"user_id":"101","amount":35000}`, `{"user_id":"101","amount":"0"}`, `{"garbage":true}`, `{} {}`} {
			if w = request("POST", "/api/expenses", body); w.Code != 400 {
				t.Fatalf("accepted bad JSON: %s: %d", body, w.Code)
			}
		}
		if w = request("GET", "/api/expenses?date_from=2026-10-18&date_to=2026-10-17", ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
		if w = request("GET", "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Семейный счёт") {
			t.Fatal("panel unavailable")
		}
	})
	t.Run("report overflow", func(t *testing.T) {
		huge := create(202, math.MaxInt64, "2026-10-17", "huge")
		if _, e = s.List(ctx, expense.Filter{}); !errors.Is(e, expense.ErrInvalid) {
			t.Fatalf("overflow: %v", e)
		}
		if e = s.Delete(ctx, 0, huge.ID); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("migration restart persistence", func(t *testing.T) {
		restart, e := postgres.Open(ctx, dsn, []int64{101, 202})
		if e != nil {
			t.Fatal(e)
		}
		defer restart.DB.Close()
		p, e := restart.List(ctx, expense.Filter{UserID: 101})
		if e != nil || p.Total < 3 {
			t.Fatal("data lost on reopen")
		}
	})
	t.Run("committed bot mutation survives failed session persistence", func(t *testing.T) { checkBotRecovery(t, s) })
	t.Run("durable delivery retry schedule and exhaustion", func(t *testing.T) { checkDeliveryOutbox(t, s) })

	t.Run("reopen complete baseline without data loss", func(t *testing.T) { checkBaselineReopen(t, s, dsn) })

	t.Run("choice list delivery and queued ordinal chain", func(t *testing.T) { checkChoiceDeliveryChain(t, s) })
	t.Run("choice acknowledgement supersession and expiry", func(t *testing.T) { checkChoiceAcknowledgement(t, s) })

	t.Run("API SQL lock deadline", func(t *testing.T) { checkAPILockDeadline(t, s, first.ID) })
	t.Run("real HTTP acceptance", func(t *testing.T) { checkHTTPAcceptance(t, s) })
	t.Run("persisted conversation acceptance", func(t *testing.T) { checkRealConversation(t, s, dsn) })
	t.Run("agent trust boundary acceptance", func(t *testing.T) { checkTrustBoundary(t, s) })
	t.Run("staff audit regressions", func(t *testing.T) { checkStaffFixes(t, s) })

}
