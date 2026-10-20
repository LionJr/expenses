package httpapi

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

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
)

type fakeStore struct{ changed bool }

func (*fakeStore) Users(context.Context) ([]expense.User, error) {
	return []expense.User{{ID: 42}}, nil
}
func (*fakeStore) List(context.Context, expense.Filter) (expense.Page, error) {
	return expense.Page{Items: []expense.Expense{}}, nil
}
func (s *fakeStore) Create(_ context.Context, i expense.Input) (expense.Expense, error) {
	s.changed = true
	return expense.Expense{ID: 1, Amount: i.Amount}, nil
}
func (s *fakeStore) Update(_ context.Context, _, _ int64, p expense.Patch) (expense.Expense, error) {
	s.changed = true
	return expense.Expense{}, nil
}
func (s *fakeStore) Delete(context.Context, int64, int64) error { s.changed = true; return nil }
func TestValidationAndCrossOrigin(t *testing.T) {
	for _, test := range []struct {
		method, path, body, origin string
		status                     int
	}{
		{http.MethodPost, "/api/expenses", `{"user_id":"42","amount":"35000","category":"еда","spent_at":"2026-10-17"}`, "", http.StatusCreated},
		{http.MethodPost, "/api/expenses", `{"user_id":"42","amount":35000}`, "", http.StatusBadRequest},
		{http.MethodPatch, "/api/expenses/1", `{}`, "", http.StatusBadRequest},
		{http.MethodPatch, "/api/expenses/1", `{"comment":""}`, "", http.StatusOK},
		{http.MethodGet, "/api/expenses?limit=101", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/expenses?offset=-1", "", "", http.StatusBadRequest},
		{http.MethodDelete, "/api/expenses/1", "", "https://other.test", http.StatusForbidden},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			s := &fakeStore{}
			handler := (&API{Store: expensesvc.New(s), RequestTimeout: 5 * time.Second, Timezone: "Europe/Moscow"}).Handler()
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", test.origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("middleware headers missing for status %d: %v", w.Code, w.Header())
			}
			if test.status >= http.StatusBadRequest && s.changed {
				t.Fatal("invalid request mutated storage")
			}
		})
	}
}

type deadlineStore struct {
	*fakeStore
	deadlineSeen bool
}

func (s *deadlineStore) List(ctx context.Context, _ expense.Filter) (expense.Page, error) {
	if _, ok := ctx.Deadline(); !ok {
		return expense.Page{}, errors.New("missing database deadline")
	}
	s.deadlineSeen = true
	<-ctx.Done()
	return expense.Page{}, ctx.Err()
}
func TestAPIDatabaseDeadline(t *testing.T) {
	store := &deadlineStore{fakeStore: &fakeStore{}}
	handler := (&API{Store: expensesvc.New(store), RequestTimeout: time.Millisecond}).Handler()
	r := httptest.NewRequest(http.MethodGet, "/api/expenses", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusGatewayTimeout || !store.deadlineSeen || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("deadline not enforced: %d %s", w.Code, w.Body)
	}
}

func (*fakeStore) Execute(context.Context, string, int64, func() (expense.Command, error)) (json.RawMessage, error) {
	panic("unexpected journal operation")
}
func (*fakeStore) OperationResult(context.Context, string, int64) (json.RawMessage, error) {
	panic("unexpected journal lookup")
}

func TestDecodeJSONMediaType(t *testing.T) {
	for _, test := range []struct {
		contentType string
		valid       bool
	}{
		{"application/json", true},
		{"Application/JSON", true},
		{"application/json; charset=utf-8", true},
		{"application/json ; charset=\"utf-8\"", true},
		{"", false},
		{"text/plain", false},
		{"application/json; charset", false},
		{"application/json; charset=\"unterminated", false},
	} {
		t.Run(test.contentType, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/expenses", strings.NewReader(`{"comment":"кофе"}`))
			r.Header.Set("Content-Type", test.contentType)
			var body struct {
				Comment string `json:"comment"`
			}
			err := decode(httptest.NewRecorder(), r, &body)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
			if test.valid && body.Comment != "кофе" {
				t.Fatalf("body was not decoded: %+v", body)
			}
		})
	}
}

func TestPanelConcurrentRendering(t *testing.T) {
	handler := (&API{Timezone: "Asia/Ashgabat"}).Handler()
	var requests sync.WaitGroup
	for range 8 {
		requests.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(w.Body.String(), `name="app-timezone" content="Asia/Ashgabat"`) {
				t.Errorf("panel rendering: status=%d, content type=%q", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}
	requests.Wait()
}

func TestFailureDoesNotExposeErrorDetails(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{expense.ErrInvalid, http.StatusBadRequest},
		{expense.ErrNotFound, http.StatusNotFound},
		{expense.ErrConflict, http.StatusConflict},
		{errors.New("storage failure"), http.StatusInternalServerError},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			w := httptest.NewRecorder()
			(&API{}).fail(w, fmt.Errorf("private-details: %w", test.err))
			if w.Code != test.status {
				t.Fatalf("status=%d, want %d", w.Code, test.status)
			}
			if strings.Contains(w.Body.String(), "private-details") || strings.Contains(w.Body.String(), "storage failure") {
				t.Fatalf("internal details exposed: %s", w.Body.String())
			}
			var response struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error.Message == "" {
				t.Fatalf("invalid error response: %s", w.Body.String())
			}
		})
	}
}
