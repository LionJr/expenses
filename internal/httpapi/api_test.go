package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/expense"
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
		{"POST", "/api/expenses", `{"user_id":"42","amount":"35000","category":"еда","spent_at":"2026-10-17"}`, "", 201},
		{"POST", "/api/expenses", `{"user_id":"42","amount":35000}`, "", 400},
		{"PATCH", "/api/expenses/1", `{}`, "", 400},
		{"PATCH", "/api/expenses/1", `{"comment":""}`, "", 200},
		{"GET", "/api/expenses?limit=101", "", "", 400},
		{"GET", "/api/expenses?offset=-1", "", "", 400},
		{"DELETE", "/api/expenses/1", "", "https://other.test", 403},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			s := &fakeStore{}
			handler := (&API{Store: s, Timezone: "Europe/Moscow"}).Handler()
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", test.origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
			if test.status >= 400 && s.changed {
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
	handler := (&API{Store: store, RequestTimeout: time.Millisecond}).Handler()
	r := httptest.NewRequest("GET", "/api/expenses", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 504 || !store.deadlineSeen || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("deadline not enforced: %d %s", w.Code, w.Body)
	}
}
