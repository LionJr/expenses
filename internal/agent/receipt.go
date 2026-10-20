package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LionJr/expenses/internal/expensewire"
)

type receiptMetadata struct {
	Operation string `json:"operation"`
}

func receipt(kind string, data json.RawMessage) string {
	var metadata receiptMetadata
	if json.Unmarshal(data, &metadata) == nil && metadata.Operation != "" {
		kind = metadata.Operation
	}
	if kind == toolDeleteExpense {
		return "расход удалён"
	}
	var e expensewire.Expense
	if json.Unmarshal(data, &e) != nil {
		return "расход сохранён"
	}
	verb := "добавлено"
	if kind == toolUpdateExpense {
		verb = "изменено"
	}
	result := fmt.Sprintf("%s: %d,%02d ₽ · %s · %s", verb, e.Amount/100, e.Amount%100, e.Category, e.SpentAt)
	if strings.TrimSpace(e.Comment) != "" {
		result += " · " + e.Comment
	}
	return result
}

func mutationKey(update int64) string { return fmt.Sprintf("telegram:%d:mutation:1", update) }
