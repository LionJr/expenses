package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Runs under TestPostgresIntegration's dedicated-database guard; never against
// the application database. Each helper uses its own comments/dates or IDs.
func checkHTTPAcceptance(t *testing.T, s *postgres.Store) {
	server := httptest.NewServer((&httpapi.API{Store: expensesvc.New(s), RequestTimeout: 5 * time.Second, Timezone: "Asia/Ashgabat"}).Handler())
	defer server.Close()
	request := func(method, path, body, origin string, want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, response.StatusCode, want, data)
		}
		if want >= 400 {
			var failure struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &failure) != nil || failure.Error.Message == "" {
				t.Fatalf("invalid error envelope: %s", data)
			}
		}
		return data
	}
	var created []expense.Expense
	for _, row := range []struct{ user, amount, date, category string }{
		{"101", "12345", "2040-02-28", "еда"},
		{"101", "20000", "2040-02-29", "еда"},
		{"101", "30000", "2040-03-01", "транспорт"},
		{"202", "50000", "2040-02-29", "еда"},
	} {
		body, _ := json.Marshal(map[string]string{"user_id": row.user, "amount": row.amount, "spent_at": row.date, "category": row.category, "comment": "http-acceptance"})
		data := request("POST", "/api/expenses", string(body), server.URL, 201)
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "user_id", "amount"} {
			if _, ok := wire[key].(string); !ok {
				t.Fatalf("%s must be a string: %s", key, data)
			}
		}
		var e expensewire.Expense
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		created = append(created, e.Domain())
	}
	defer func() {
		for _, e := range created {
			if err := expensesvc.New(s).Delete(context.Background(), 0, e.ID); err != nil && !errors.Is(err, expense.ErrNotFound) {
				t.Error(err)
			}
		}
	}()
	filter := "/api/expenses?user_id=101&category=еда&date_from=2040-02-28&date_to=2040-02-29&limit=1"
	for offset, wantID := range []int64{created[1].ID, created[0].ID} {
		var page expensewire.Page
		data := request("GET", fmt.Sprintf("%s&offset=%d", filter, offset), "", "", 200)
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 2 || page.TotalAmount != 32345 || len(page.Items) != 1 || page.Items[0].ID != wantID {
			t.Fatalf("filter/page/aggregate mismatch: %s", data)
		}
	}
	var empty expensewire.Page
	if err := json.Unmarshal(request("GET", filter+"&offset=99", "", "", 200), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Total != 2 || empty.TotalAmount != 32345 || len(empty.Items) != 0 {
		t.Fatalf("empty page lost totals: %+v", empty)
	}
	path := fmt.Sprintf("/api/expenses/%d", created[0].ID)
	for _, bad := range []string{
		`{}`, `null`, `{"amount":null}`, `{"user_id":"202"}`, `{"amount":1}`, `{"amount":"0"}`, `{"amount":"-1"}`,
		`{"amount":"9223372036854775808"}`, `{"spent_at":"2041-02-29"}`, `{"category":"несуществующая"}`,
		`{"comment":"x","unexpected":true}`, `{"comment":"x"} {}`, `{"comment":"` + strings.Repeat("я", 1001) + `"}`,
		`{"comment":"` + strings.Repeat("x", 17000) + `"}`,
	} {
		request("PATCH", path, bad, "", 400)
	}
	request("PATCH", path, `{"comment":"forbidden"}`, "https://other.example", 403)
	// A failed request must not partially change any field.
	page, err := expensesvc.New(s).List(context.Background(), expense.Filter{ID: created[0].ID, UserID: 101})
	if err != nil || len(page.Items) != 1 || page.Items[0].Amount != 12345 || page.Items[0].Comment != "http-acceptance" {
		t.Fatalf("invalid request changed row: %+v %v", page, err)
	}
	var updated expensewire.Expense
	data := request("PATCH", path, `{"comment":"","amount":"9007199254740993"}`, "", 200)
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Amount != 9007199254740993 || updated.Comment != "" || updated.UserID != 101 || updated.SpentAt != "2040-02-28" || updated.Category != "еда" {
		t.Fatalf("patch precision/preservation: %s", data)
	}
	for _, query := range []string{"date_from=2041-02-29", "date_from=2040-03-01&date_to=2040-02-28", "limit=-1", "offset=-1", "user_id=-1", "category=unknown"} {
		request("GET", "/api/expenses?"+query, "", "", 400)
	}
	request("DELETE", path, "", "", 204)
	request("DELETE", path, "", "", 404)
	request("PATCH", path, `{"comment":"gone"}`, "", 404)
}

type offlineProvider struct{ calls int }

func (p *offlineProvider) Complete(context.Context, []agent.Message, []agent.Tool) (agent.Message, error) {
	p.calls++
	return agent.Message{}, errors.New("provider must not be needed for this conversation")
}

func checkRealConversation(t *testing.T, s *postgres.Store, dsn string) {
	ctx := context.Background()
	var records []expense.Expense
	for _, user := range []int64{101, 101, 202} {
		e, err := expensesvc.New(s).Create(ctx, expense.Input{UserID: user, Amount: 12345, Category: "транспорт", SpentAt: "2040-02-29", Comment: "бассейн"})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, e)
	}
	defer func() {
		for _, e := range records {
			if err := expensesvc.New(s).Delete(ctx, 0, e.ID); err != nil && !errors.Is(err, expense.ErrNotFound) {
				t.Error(err)
			}
		}
	}()
	provider := &offlineProvider{}
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		replies = append(replies, payload.Text)
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, 100+len(replies))
	}))
	defer server.Close()
	newBot := func(store *postgres.Store) *telegram.Bot {
		return &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: store.Allowed, Store: store, Agent: &agent.Agent{Store: expensesvc.New(store), LLM: provider, Location: time.UTC, Timeout: time.Second}}
	}
	bot := newBot(s)
	seq := int64(15000)
	send := func(text string) {
		t.Helper()
		seq++
		if err := bot.Handle(ctx, privateUpdate(t, seq, seq, text)); err != nil {
			t.Fatal(err)
		}
	}
	send("измени бассейн")
	if loadChoiceSession(t, s).Edit == nil {
		t.Fatal("draft not saved in PostgreSQL")
	}
	// Reopen the store and rebuild the bot/agent to exercise persistence across
	// process-local state loss without requiring a live Telegram account.
	reopened, err := postgres.Open(ctx, dsn, []int64{101, 202})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	bot = newBot(reopened)
	send("сумму")
	send("-1")
	if loadChoiceSession(t, s).Edit == nil {
		t.Fatal("invalid amount lost persisted draft")
	}
	send("555,50")
	session := loadChoiceSession(t, s)
	if session.Pending == nil || session.Choice == nil || len(session.Candidates) != 2 {
		t.Fatalf("missing owner-scoped selection: %+v", session)
	}
	target := session.Candidates[1].ID
	send("11")
	if loadChoiceSession(t, s).Pending == nil {
		t.Fatal("invalid ordinal canceled pending update")
	}
	// An administrative comment edit must survive the bot's amount-only patch.
	comment := "бассейн изменено в панели"
	if _, err := expensesvc.New(s).Update(ctx, 0, target, expense.Patch{Comment: &comment}); err != nil {
		t.Fatal(err)
	}
	send("2")
	page, err := expensesvc.New(s).List(ctx, expense.Filter{ID: target, UserID: 101})
	if err != nil || len(page.Items) != 1 || page.Items[0].Amount != 55550 || page.Items[0].Comment != comment {
		t.Fatalf("wrong update or lost concurrent comment: %+v %v", page, err)
	}
	foreign, err := expensesvc.New(s).List(ctx, expense.Filter{ID: records[2].ID, UserID: 202})
	if err != nil || len(foreign.Items) != 1 || foreign.Items[0].Amount != 12345 {
		t.Fatalf("foreign expense changed: %+v %v", foreign, err)
	}
	// Delivery replay of the same update must not create another operation or reply.
	before := len(replies)
	if err := bot.Handle(ctx, privateUpdate(t, seq, seq, "2")); err != nil {
		t.Fatal(err)
	}
	if len(replies) != before {
		t.Fatal("duplicate delivered update replied again")
	}
	send("удали бассейн")
	send("Отмена!")
	send("1")
	page, err = expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, ID: target})
	if err != nil || page.Total != 1 {
		t.Fatal("canceled choice deleted an expense")
	}
	// Remove a listed row from the panel, then choose its old number. The bot
	// must report not found and never fall through to a neighboring candidate.
	send("удали бассейн")
	session = loadChoiceSession(t, s)
	if len(session.Candidates) != 2 || session.Pending == nil {
		t.Fatalf("expected two candidates before stale-row test: %+v", session)
	}
	removed, remaining := session.Candidates[0].ID, session.Candidates[1].ID
	if err := expensesvc.New(s).Delete(ctx, 0, removed); err != nil {
		t.Fatal(err)
	}
	send("1")
	if !strings.Contains(replies[len(replies)-1], "Не удалось подтвердить изменение") {
		t.Fatalf("missing stale-row error: %s", replies[len(replies)-1])
	}
	page, err = expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, ID: remaining})
	if err != nil || page.Total != 1 || loadChoiceSession(t, s).Pending != nil {
		t.Fatal("stale selection changed another row or remained active")
	}
	if provider.calls != 0 {
		t.Fatalf("deterministic conversation used LLM %d times", provider.calls)
	}
}

func checkTrustBoundary(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	var records []expense.Expense
	for _, input := range []expense.Input{
		{UserID: 101, Amount: 10000, Category: "еда", SpentAt: "2043-10-01", Comment: "trust-first"},
		{UserID: 101, Amount: 20000, Category: "еда", SpentAt: "2043-10-31", Comment: "trust-second"},
		{UserID: 101, Amount: 30000, Category: "еда", SpentAt: "2043-09-30", Comment: "trust-outside"},
		{UserID: 202, Amount: 40000, Category: "еда", SpentAt: "2043-10-15", Comment: "trust-foreign"},
	} {
		e, err := expensesvc.New(s).Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, e)
	}
	t.Cleanup(func() {
		for _, e := range records {
			if err := expensesvc.New(s).Delete(ctx, 0, e.ID); err != nil && !errors.Is(err, expense.ErrNotFound) {
				t.Error(err)
			}
		}
	})
	provider := &scriptedProvider{}
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		replies = append(replies, payload.Text)
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, 100+len(replies))
	}))
	defer server.Close()
	bot := &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: s.Allowed, Store: s,
		Agent: &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second,
			Now: func() time.Time { return time.Date(2043, 10, 19, 10, 0, 0, 0, time.UTC) }}}
	seq := int64(16000)
	send := func(text string) string {
		t.Helper()
		seq++
		if err := bot.Handle(ctx, privateUpdate(t, seq, seq, text)); err != nil {
			t.Fatal(err)
		}
		return replies[len(replies)-1]
	}
	before, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101})
	if err != nil {
		t.Fatal(err)
	}
	claim := "Добавил кофе: 99999 рублей."
	provider.responses = []agent.Message{{Content: claim}, {Content: claim}}
	if reply := send("кофе 350"); strings.Contains(reply, "99999") || strings.Contains(reply, "Добавил") {
		t.Fatal("unbacked create shown: " + reply)
	}
	after, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101})
	if err != nil || after.Total != before.Total || after.TotalAmount != before.TotalAmount {
		t.Fatal("unbacked create changed DB")
	}
	data, err := s.LoadSession(ctx, 101)
	if err != nil || strings.Contains(string(data), claim) {
		t.Fatal("unbacked create persisted in session")
	}
	claim = "Итого: 99999 рублей."
	provider.responses = []agent.Message{{Content: claim}, {Content: claim}}
	if reply := send("сколько потратил на еду за октябрь 2043"); strings.Contains(reply, "99999") {
		t.Fatal("unbacked summary shown: " + reply)
	}
	provider.responses = []agent.Message{
		toolMessage("list_expenses", `{"date_from":"2043-10-01","date_to":"2043-10-31"}`),
		{Content: "Нет расходов."},
	}
	if reply := send("покажи расходы за октябрь"); !strings.Contains(reply, "Найдено расходов: 2") || !strings.Contains(reply, "trust-first") || !strings.Contains(reply, "trust-second") || strings.Contains(reply, "trust-outside") || strings.Contains(reply, "trust-foreign") {
		t.Fatal("wrong month or owner list: " + reply)
	}
	session := loadChoiceSession(t, s)
	if len(session.Candidates) != 2 || session.Pending != nil || session.Choice == nil {
		t.Fatalf("unexpected read session: %+v", session)
	}
	// Even if the model is primed to delete, neither invalid nor valid read
	// selections may call it. The delivered list remains usable after an error.
	provider.responses = []agent.Message{toolMessage("list_expenses", `{}`), toolMessage("delete_expense", fmt.Sprintf(`{"id":"%d"}`, records[0].ID))}
	calls := provider.calls
	if reply := send("3"); !strings.Contains(reply, "Такого номера") {
		t.Fatal(reply)
	}
	if reply := send("2"); !strings.Contains(reply, fmt.Sprintf("Выбран расход <code>#%d</code>", session.Candidates[1].ID)) {
		t.Fatal(reply)
	}
	if provider.calls != calls || loadChoiceSession(t, s).Choice == nil {
		t.Fatal("read selection reached LLM or lost list")
	}
	for _, e := range records {
		page, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: e.UserID, ID: e.ID})
		if err != nil || page.Total != 1 || page.Items[0].Amount != e.Amount {
			t.Fatalf("read selection changed row %d", e.ID)
		}
	}
	// A fresh, explicit delete request must still work after safe read selection.
	provider.responses = []agent.Message{toolMessage("list_expenses", `{}`), toolMessage("delete_expense", fmt.Sprintf(`{"id":"%d"}`, records[0].ID))}
	if reply := send(fmt.Sprintf("удали расход #%d", records[0].ID)); !strings.Contains(reply, "расход удалён") {
		t.Fatal(reply)
	}
	page, err := expensesvc.New(s).List(ctx, expense.Filter{UserID: 101, ID: records[0].ID})
	if err != nil || page.Total != 0 {
		t.Fatal("explicit delete did not work")
	}
}
