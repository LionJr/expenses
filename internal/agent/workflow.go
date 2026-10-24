package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func (a *Agent) resolveRequest(ctx context.Context, user, update int64, text string, s Session, permission requestPermissions, started time.Time) (preparedRequest, error) {
	if token, ok := previousAmount(a.Currency, text); ok && permission.Mutation == toolUpdateExpense {
		if _, err := expense.ParseAmount(token); err != nil {
			clearSelection(&s)
			answer := "Прежняя сумма должна быть положительной и содержать не более двух знаков после запятой. Ничего не изменено."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
	}
	if token, compactEntry := a.Currency.EntryAmount(text); compactEntry && !selectionReply(text) {
		if _, amountErr := expense.ParseAmount(token); amountErr != nil {
			clearSelection(&s)
			answer := "Сумма расхода должна быть больше нуля и содержать не более двух знаков после запятой. Ничего не изменено."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
	}
	if token, ok := previousAmount(a.Currency, text); ok && latestRequested(text) {
		expected, parseErr := expense.ParseAmount(token)
		if parseErr == nil {
			page, lookupErr := a.Store.List(ctx, expense.Filter{UserID: user, Latest: true, Limit: 1})
			if lookupErr != nil {
				return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: toolListExpenses, cause: lookupErr}
			}
			if len(page.Items) == 0 || page.Items[0].Amount != expected {
				clearSelection(&s)
				answer := "Расходов для изменения не найдено."
				if len(page.Items) > 0 {
					e := page.Items[0]
					answer = fmt.Sprintf("Прежняя сумма не совпадает: последний добавленный расход — %s · %s · %s (#%d). Ничего не изменено. Уточните, какой расход нужно изменить.", a.Currency.FormatAmount(e.Amount), e.Category, e.Comment, e.ID)
				}
				remember(&s, text, answer)
				return preparedRequest{session: s, answer: answer, handled: true}, nil
			}
		}
	}
	if id, explicit := requestedExpenseID(text); explicit && !selectionReply(text) && !showExpenseID.MatchString(text) {
		page := expense.Page{}
		if id > 0 {
			var lookupErr error
			page, lookupErr = a.Store.List(ctx, expense.Filter{ID: id, UserID: user, Limit: 1})
			if lookupErr != nil {
				return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: toolListExpenses, cause: lookupErr}
			}
		}
		if len(page.Items) == 0 {
			clearSelection(&s)
			answer := "Расход не найден среди ваших расходов."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
	}
	if match := showExpenseID.FindStringSubmatch(text); len(match) > 0 {
		clearSelection(&s)
		id, parseErr := strconv.ParseInt(match[1], 10, 64)
		answer := "Расход не найден среди ваших расходов."
		if parseErr == nil {
			page, lookupErr := a.Store.List(ctx, expense.Filter{ID: id, UserID: user, Limit: 1})
			if lookupErr != nil {
				return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: toolListExpenses, cause: lookupErr}
			}
			if len(page.Items) > 0 {
				s.Candidates, s.CandidateTotal = page.Items, page.Total
				answer = choicePrompt(a.Currency, s)
				prepareChoice(&s, update)
			}
		}
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}, nil
	}
	if selectionReply(text) {
		if len(s.Candidates) == 0 {
			clearSelection(&s)
			answer := "Нет активного списка для выбора. Сначала найдите нужные расходы."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
		if s.Choice == nil {
			// Legacy sessions cannot prove which list was sent. Show it afresh first.
			prepareChoice(&s, update)
			answer := "Сначала покажу актуальный список. После его получения отправьте номер ещё раз.\n" + choicePrompt(a.Currency, s)
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
		if s.Choice.State != botstate.AwaitingSelection {
			answer := "Список ещё не отправлен. Дождитесь его доставки и затем выберите расход."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
	}
	selected := selectCandidate(text, s.Candidates)
	s.LatestID = 0
	if selectionReply(text) {
		if selected == 0 {
			answer := "Такого номера нет в списке.\nОтветьте номером из последнего списка или #ID. Отменить — «отмена»."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
		if s.Pending == nil {
			// A list is not permission to mutate. Never ask the model to infer an
			// old operation from history when this list has no pending action.
			answer := fmt.Sprintf("Выбран расход #%d. Чтобы изменить или удалить его, отправьте полный запрос, например «удали расход #%d».", selected, selected)
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}, nil
		}
		action := s.Pending
		patch := action.Patch
		patch.ExpectedAmount = action.ExpectedAmount
		result, e := a.executeMutation(ctx, update, user, action.Kind, selected, expense.Input{}, patch)
		answer := ""
		if e != nil {
			logFailure(update, action.Kind, e, time.Since(started))
			if !errors.Is(e, expense.ErrInvalid) && !errors.Is(e, expense.ErrNotFound) {
				return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: action.Kind, cause: e}
			}
			answer = "Не удалось подтвердить изменение. " + safeError(e)
		} else {
			answer = "Выполнено: " + receipt(a.Currency, action.Kind, result)
		}
		if e == nil || errors.Is(e, expense.ErrNotFound) {
			clearSelection(&s)
		}
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}, nil
	}
	if selected == 0 {
		clearSelection(&s)
	}
	if target := simpleListSearch(text); target != "" {
		page, err := a.Store.List(ctx, expense.Filter{UserID: user, Search: target, Limit: 10})
		if err != nil {
			return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: toolListExpenses, cause: err}
		}
		s.Candidates, s.CandidateTotal = page.Items, page.Total
		answer := "По заданным фильтрам расходов не найдено."
		if len(page.Items) > 0 {
			answer = choicePrompt(a.Currency, s)
			prepareChoice(&s, update)
		}
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}, nil
	}
	search := simpleDeleteSearch(text)
	kind := toolDeleteExpense
	patch := expense.Patch{}
	if match := simpleUpdate.FindStringSubmatch(text); len(match) > 0 && !latestRequested(text) {
		// Reuse the conservative single-target validation used by deletion.
		if target := simpleDeleteSearch("удали " + match[1]); target != "" {
			if match[2] == "" {
				s.Edit = &EditDraft{Search: target}
				answer := "Что изменить: сумму, дату, категорию или комментарий?\nДля суммы ответьте «сумму». Для других полей напишите полный запрос, например «измени дату " + target + " на вчера».\nОтменить — «отмена»."
				remember(&s, text, answer)
				return preparedRequest{session: s, answer: answer, handled: true}, nil
			}
			amount, err := expense.ParseAmount(match[2])
			if err != nil {
				answer := "Сумма должна быть больше нуля и содержать не более двух знаков после запятой. Ничего не изменено."
				remember(&s, text, answer)
				return preparedRequest{session: s, answer: answer, handled: true}, nil
			}
			search, kind, patch.Amount = target, toolUpdateExpense, &amount
		}
	}
	if search != "" {
		page, err := a.Store.List(ctx, expense.Filter{UserID: user, Search: search, Limit: 10})
		if err != nil {
			return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: toolListExpenses, cause: err}
		}
		s.Candidates, s.CandidateTotal = page.Items, page.Total
		answer := "По заданным фильтрам расходов не найдено."
		if len(page.Items) > 0 {
			if page.Total > 1 {
				s.Pending = &PendingAction{Kind: kind, Patch: patch, IntentValidated: true}
				prepareChoice(&s, update)
				answer = choicePrompt(a.Currency, s)
			} else {
				result, err := a.executeMutation(ctx, update, user, kind, page.Items[0].ID, expense.Input{}, patch)
				if err != nil {
					if !errors.Is(err, expense.ErrNotFound) && !errors.Is(err, expense.ErrInvalid) {
						return preparedRequest{session: s, handled: true}, &ProcessingError{Operation: kind, cause: err}
					}
					answer = "Не удалось подтвердить изменение. " + safeError(err)
				} else {
					answer = "Выполнено: " + receipt(a.Currency, kind, result)
				}
				clearSelection(&s)
			}
		}
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}, nil
	}

	return preparedRequest{text: text, session: s, permission: permission, selected: selected}, nil
}
