package postgres

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensewire"
)

func TestOperationReceiptJSONCompatibility(t *testing.T) {
	// Literal fixtures describe the already persisted format independently of the
	// new receipt structs. Large IDs and amounts must remain decimal strings.
	const expenseJSON = `{"operation":"create_expense","id":"9007199254740993","user_id":"42","amount":"9223372036854775807","version":"7","category":"еда","spent_at":"2026-10-17","comment":"кофе","created_at":"2026-10-17T10:00:00Z","updated_at":"2026-10-17T10:00:00Z"}`
	timestamp := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	for _, operation := range []expense.Operation{expense.Create, expense.Update} {
		t.Run(string(operation), func(t *testing.T) {
			var expected map[string]any
			if err := json.Unmarshal([]byte(expenseJSON), &expected); err != nil {
				t.Fatal(err)
			}
			expected["operation"] = string(operation)
			value := expenseReceipt{Operation: operation, Expense: expensewire.FromExpense(expense.Expense{
				ID: 9007199254740993, UserID: 42, Amount: math.MaxInt64, Version: 7,
				Category: "еда", SpentAt: "2026-10-17", Comment: "кофе", CreatedAt: timestamp, UpdatedAt: timestamp,
			})}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err = json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("receipt format changed: %s", data)
			}
		})
	}
	data, err := json.Marshal(deleteReceipt{Operation: expense.Delete, DeletedID: 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"operation": "delete_expense", "deleted_id": "9007199254740993"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("delete receipt format changed: %s", data)
	}
}
