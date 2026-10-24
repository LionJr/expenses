package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/LionJr/expenses/internal/expense"
)

type preparedRequest struct {
	text       string
	session    Session
	permission requestPermissions
	selected   int64
	answer     string
	handled    bool
	create     *expense.Input
}

func prepareRequest(c expense.Currency, text string, s Session) preparedRequest {
	if err := expense.Text(text); err != nil {
		clearSelection(&s)
		answer := "Текст содержит недопустимый символ. Отправьте запрос без него."
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	if utf8.RuneCountInString(text) > 2000 {
		return preparedRequest{session: s, answer: "Сообщение слишком длинное. Сократите его до 2000 символов.", handled: true}
	}
	if cancelReply(text) {
		clearSelection(&s)
		answer := "Запрос отменён."
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	// Pending actions created before intent checks were introduced cannot grant
	// write permission after an upgrade. Keep their list available for viewing.
	if s.Pending != nil && !s.Pending.IntentValidated {
		s.Pending = nil
	}
	if explicitProhibition.MatchString(text) {
		clearSelection(&s)
		answer := "Ничего не изменено. Ожидающее действие отменено."
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	if c.HasForeignAmount(text) {
		clearSelection(&s)
		answer := "Приложение использует " + c.Code() + ". Укажите сумму в этой валюте; автоматического пересчёта нет."
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	var continuation *requestPermissions
	if draft := s.Request; draft != nil {
		incoming := permissionsFor(c, text)
		if incoming.Mutation == "" && !incoming.ReadOnly {
			permission := draft.Permissions
			value := strings.TrimSpace(text)
			if draft.Field == "category" && permission.Mutation == toolCreateExpense && !permission.ReadOnly {
				if input, ok := simpleEntry(draft.Text); ok {
					category := strings.ToLower(value)
					if !expense.Category(category) {
						answer, _ := clarification(c, "category")
						remember(&s, text, answer)
						return preparedRequest{session: s, answer: answer, handled: true}
					}
					input.Category, input.SpentAt = category, draft.SpentAt
					s.Request = nil
					return preparedRequest{text: draft.Text + "\nКатегория: " + category, session: s, permission: permission, create: &input}
				}
			}
			if draft.Field == "amount" {
				if _, err := expense.ParseAmount(value); err != nil {
					answer := "Введите положительную сумму в " + c.Code() + ", до двух знаков после запятой, например 350,50. Отменить — «отмена»."
					remember(&s, text, answer)
					return preparedRequest{session: s, answer: answer, handled: true}
				}
				if permission.Mutation == "" && !permission.ReadOnly && simpleDescription.MatchString(draft.Text) {
					permission = requestPermissions{Mutation: toolCreateExpense}
				}
				switch permission.Mutation {
				case toolCreateExpense:
					text = draft.Text + " " + value
				case toolUpdateExpense:
					text = draft.Text + " на " + value
				default:
					text = draft.Text + "\nУточнение суммы: " + value
				}
			} else {
				text = draft.Text + "\nУточнение (" + draft.Field + "): " + value
			}
			continuation = &permission
		}
		s.Request = nil
	}
	if s.Edit != nil {
		draft := s.Edit
		value := strings.TrimSpace(text)
		if strings.EqualFold(value, "сумму") || strings.EqualFold(value, "сумма") {
			draft.Field = "amount"
			answer := "Укажите новую сумму в " + c.Code() + ", например 500,50.\nОтменить — «отмена»."
			remember(&s, text, answer)
			return preparedRequest{session: s, answer: answer, handled: true}
		}
		if amountReply.MatchString(value) {
			if _, err := expense.ParseAmount(value); err != nil {
				answer := "Сумма должна быть больше нуля, до двух знаков после запятой.\nВведите сумму, например 500,50, или «отмена»."
				remember(&s, text, answer)
				return preparedRequest{session: s, answer: answer, handled: true}
			}
			text = "измени " + draft.Search + " на " + value
		}
		s.Edit = nil
	}
	if utf8.RuneCountInString(text) > 2000 {
		clearSelection(&s)
		answer := "Уточнений слишком много. Отправьте полный запрос одним сообщением."
		remember(&s, truncate(text, 2000), answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	// Multiple explicit targets must never be reduced to the one chosen by the
	// model. Apply this after merging clarifications and before any search/write.
	if multipleExpenseIDs(text) {
		clearSelection(&s)
		answer := "Укажите только один расход: например, «удали расход #17». Несколько расходов отправьте отдельными сообщениями. Ничего не изменено."
		remember(&s, text, answer)
		return preparedRequest{session: s, answer: answer, handled: true}
	}
	permission := permissionsFor(c, text)
	if continuation != nil {
		permission = *continuation
	}
	return preparedRequest{text: text, session: s, permission: permission}
}
