package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/LionJr/expenses/internal/expense"
)

// Restrict the shortcut to one description word and one positive amount.
// Dates, multiple expenses, and other instructions still require interpretation.
var simpleEntryPattern = regexp.MustCompile(`^([\p{L}]+)\s+([0-9]+(?:[.,][0-9]{1,2})?)$`)

func simpleEntry(text string) (expense.Input, bool) {
	m := simpleEntryPattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return expense.Input{}, false
	}
	amount, err := expense.ParseAmount(m[2])
	if err != nil {
		return expense.Input{}, false
	}
	return expense.Input{Amount: amount, Comment: m[1]}, true
}

func entryCategory(description string) string {
	switch strings.ToLower(description) {
	case "kofe", "doner", "döner":
		return "еда"
	case "taksi":
		return "транспорт"
	case "koynek", "köýnek":
		return "покупки"
	}
	return ""
}

func (a *Agent) entryDate() string {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	return now.In(a.Location).Format("2006-01-02")
}

func (a *Agent) createEntry(ctx context.Context, user, update int64, request preparedRequest, started time.Time) (string, Session, error) {
	input := *request.create
	input.UserID = user
	if input.SpentAt == "" {
		input.SpentAt = a.entryDate()
	}
	result, err := a.executeMutation(ctx, update, user, toolCreateExpense, 0, input, expense.Patch{})
	if err != nil {
		logFailure(update, toolCreateExpense, err, time.Since(started))
		if !errors.Is(err, expense.ErrInvalid) && !errors.Is(err, expense.ErrNotFound) {
			return "", request.session, &ProcessingError{Operation: toolCreateExpense, cause: err}
		}
		answer := "Не удалось подтвердить изменение. " + safeError(err)
		remember(&request.session, request.text, answer)
		return answer, request.session, nil
	}
	clearSelection(&request.session)
	answer := "Выполнено: " + receipt(a.Currency, toolCreateExpense, result)
	remember(&request.session, request.text, answer)
	return answer, request.session, nil
}
