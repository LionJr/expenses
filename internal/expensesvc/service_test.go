package expensesvc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
)

type recordingRepository struct {
	calls    int
	filter   expense.Filter
	command  expense.Command
	prepared int
	saved    json.RawMessage
	failure  error
}

func (r *recordingRepository) Users(context.Context) ([]expense.User, error) {
	r.calls++
	return nil, r.failure
}
func (r *recordingRepository) List(_ context.Context, f expense.Filter) (expense.Page, error) {
	r.calls++
	r.filter = f
	return expense.Page{}, r.failure
}
func (r *recordingRepository) Create(_ context.Context, i expense.Input) (expense.Expense, error) {
	r.calls++
	r.command = expense.Command{Operation: expense.Create, Input: i}
	return expense.Expense{}, r.failure
}
func (r *recordingRepository) Update(_ context.Context, owner, id int64, p expense.Patch) (expense.Expense, error) {
	r.calls++
	r.command = expense.Command{Operation: expense.Update, ID: id, Patch: p}
	return expense.Expense{}, r.failure
}
func (r *recordingRepository) Delete(_ context.Context, owner, id int64) error {
	r.calls++
	r.command = expense.Command{Operation: expense.Delete, ID: id}
	return r.failure
}
func (r *recordingRepository) OperationResult(context.Context, string, int64) (json.RawMessage, error) {
	r.calls++
	return r.saved, r.failure
}
func (r *recordingRepository) Execute(_ context.Context, _ string, _ int64, prepare func() (expense.Command, error)) (json.RawMessage, error) {
	r.calls++
	if r.failure != nil {
		return nil, r.failure
	}
	if r.saved != nil {
		return r.saved, nil
	}
	r.prepared++
	c, err := prepare()
	if err != nil {
		return nil, err
	}
	r.command = c
	return json.RawMessage(`{"operation":"create_expense","id":"1"}`), nil
}

func TestInvalidExpenseRequestsNeverReachRepository(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Service) error
	}{
		{"invalid filter", func(s *Service) error { _, err := s.List(context.Background(), expense.Filter{Limit: 101}); return err }},
		{"invalid create", func(s *Service) error {
			_, err := s.Create(context.Background(), expense.Input{UserID: 42, Amount: -1})
			return err
		}},
		{"invalid update", func(s *Service) error { _, err := s.Update(context.Background(), 0, 1, expense.Patch{}); return err }},
		{"invalid update id", func(s *Service) error {
			n := int64(100)
			_, err := s.Update(context.Background(), 0, 0, expense.Patch{Amount: &n})
			return err
		}},
		{"invalid delete", func(s *Service) error { return s.Delete(context.Background(), 0, 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &recordingRepository{}
			if err := test.run(New(r)); !errors.Is(err, expense.ErrInvalid) {
				t.Fatalf("expected validation error, got %v", err)
			}
			if r.calls != 0 {
				t.Fatalf("invalid request reached persistence: %d", r.calls)
			}
		})
	}
}

func TestListNormalizesBeforeRepositoryWithoutChangingCaller(t *testing.T) {
	for _, limit := range []int{0, 1, 100} {
		r := &recordingRepository{}
		f := expense.Filter{UserID: 42, Limit: limit, Offset: 3, Search: "кофе"}
		expected := f
		if limit == 0 {
			expected.Limit = 20
		}
		if _, err := New(r).List(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		if r.calls != 1 || r.filter != expected || f.Limit != limit {
			t.Fatalf("wrong normalized filter: %+v, input %+v", r.filter, f)
		}
	}
}

func TestExecutePreparesOnlyFreshOperations(t *testing.T) {
	const saved = `{"operation":"delete_expense","deleted_id":"17"}`
	r := &recordingRepository{saved: json.RawMessage(saved)}
	got, err := New(r).Execute(context.Background(), "committed", 42, expense.Command{})
	if err != nil || string(got) != saved || r.prepared != 0 {
		t.Fatalf("replay was validated: %s %v, prepares %d", got, err, r.prepared)
	}
	r.saved = nil
	if _, err := New(r).Execute(context.Background(), "fresh", 42, expense.Command{}); !errors.Is(err, expense.ErrInvalid) || r.prepared != 1 {
		t.Fatalf("fresh command was not validated: %v", err)
	}
}

func TestExecuteUsesAuthenticatedOwnerBeforeValidation(t *testing.T) {
	r := &recordingRepository{}
	c := expense.Command{Operation: expense.Create, Input: expense.Input{UserID: -1, Amount: 100, Category: "еда", SpentAt: "2026-10-17"}}
	if _, err := New(r).Execute(context.Background(), "fresh", 42, c); err != nil {
		t.Fatal(err)
	}
	if r.command.Input.UserID != 42 || c.Input.UserID != -1 || r.prepared != 1 {
		t.Fatalf("owner not applied safely: %+v", r.command)
	}
}

func TestExecuteRejectsInvalidCommandWithoutMutation(t *testing.T) {
	for _, c := range []expense.Command{
		{Operation: expense.Create, Input: expense.Input{Amount: 100, Category: "нет", SpentAt: "2026-10-17"}},
		{Operation: expense.Update, ID: 1, Patch: expense.Patch{}},
		{Operation: expense.Delete, ID: 0},
	} {
		r := &recordingRepository{}
		if _, err := New(r).Execute(context.Background(), "fresh", 42, c); !errors.Is(err, expense.ErrInvalid) {
			t.Fatalf("invalid command accepted: %+v %v", c, err)
		}
		if !reflect.DeepEqual(r.command, expense.Command{}) {
			t.Fatalf("invalid command applied: %+v", r.command)
		}
	}
}

func TestServicePreservesStorageErrors(t *testing.T) {
	failure := errors.New("storage unavailable")
	r := &recordingRepository{failure: failure}
	ctx := context.Background()
	if _, err := New(r).List(ctx, expense.Filter{}); err != failure {
		t.Fatalf("storage error changed: %v", err)
	}
	if _, err := New(r).Execute(ctx, "fresh", 42, expense.Command{}); err != failure || r.prepared != 0 {
		t.Fatalf("storage error masked by validation: %v", err)
	}
}
