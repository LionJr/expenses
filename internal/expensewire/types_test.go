package expensewire

import (
	"encoding/json"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
)

func TestEmptyPageJSON(t *testing.T) {
	for _, items := range [][]expense.Expense{nil, {}} {
		data, err := json.Marshal(FromPage(expense.Page{Items: items}))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"items":[],"total":0,"total_amount":"0"}` {
			t.Fatalf("unexpected empty page JSON: %s", data)
		}
	}
}
