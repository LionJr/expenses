package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/expense"
)

type Repository interface {
	ValidateCommand(expense.Command) error
	List(context.Context, expense.Filter) (expense.Page, error)
	OperationResult(context.Context, string, int64) (json.RawMessage, error)
	Execute(context.Context, string, int64, expense.Command) (json.RawMessage, error)
}

type Agent struct {
	Store    Repository
	LLM      Completer
	Location *time.Location
	Timeout  time.Duration
	Now      func() time.Time
}

const fallback = "Не удалось завершить запрос к ИИ. Если операция уже выполнена, она указана ниже. Посмотрите расходы перед новой попыткой."

var (
	oldAmount         = regexp.MustCompile(`(?i)(?:^|[^\p{L}])не\s+([-−+]?[0-9][0-9.,]*(?:e[-+]?[0-9]+)?)`)
	listRequest       = regexp.MustCompile(`(?i)(?:покажи|показать|покажите|список)[\s\S]*(?:трат|расход)`)
	showExpenseID     = regexp.MustCompile(`(?i)^\s*(?:покажи|показать|покажите|найди)\s+(?:расход|трату)\s+#([1-9][0-9]*)[.!?]?\s*$`)
	explicitExpenseID = regexp.MustCompile(`#([0-9]+)`)
	// A compact expense entry is an amount, not a command to edit or delete.
	expenseEntry = regexp.MustCompile(`(?i)^\s*[\p{L}][\p{L}\s]*\s+([-−]?[0-9]+(?:[.,][0-9]+)?)(?:\s*(?:₽|руб(?:лей|ля|ль)?\.?))?(?:\s+(?:сегодня|вчера))?\s*$`)
)

func (a *Agent) Reply(ctx context.Context, user, update int64, text string, s Session) (string, Session, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()

	started := time.Now()
	saved, err := a.Store.OperationResult(ctx, mutationKey(update), user)
	if err != nil {
		logFailure(update, "operation_lookup", err, time.Since(started))
		return "", s, &ProcessingError{Operation: "operation_lookup", cause: err}
	}
	if len(saved) > 0 {
		clearSelection(&s)
		answer := "Выполнено: " + receipt("", saved)
		remember(&s, text, answer)
		slog.Info("agent mutation recovered", "update_id", update, "duration_ms", time.Since(started).Milliseconds())
		return answer, s, nil
	}
	request := prepareRequest(text, s)
	if request.handled {
		return request.answer, request.session, nil
	}
	request, err = a.resolveRequest(ctx, user, update, request.text, request.session, request.permission, started)
	if err != nil || request.handled {
		return request.answer, request.session, err
	}
	return a.runModel(ctx, user, update, request.text, request.session, request.permission, request.selected, started)
}

func multipleExpenseIDs(text string) bool {
	return len(explicitExpenseID.FindAllStringIndex(text, 2)) > 1
}
func requestedExpenseID(text string) (int64, bool) {
	matches := explicitExpenseID.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	if len(matches) > 1 {
		return 0, true
	}
	id, err := strconv.ParseInt(matches[0][1], 10, 64)
	if err != nil || id <= 0 {
		return 0, true
	}
	return id, true
}
func historySize(m []Message) int {
	n := 0
	for _, v := range m {
		n += len(v.Content)
	}
	return n
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func isMutation(n string) bool {
	return n == toolCreateExpense || n == toolUpdateExpense || n == toolDeleteExpense
}
func safeError(err error) string {
	if errors.Is(err, expense.ErrInvalid) || errors.Is(err, expense.ErrNotFound) {
		return safeText(err.Error())
	}
	return "Ошибка хранилища. Операция не подтверждена."
}
