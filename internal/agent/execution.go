package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensewire"
)

type arguments struct {
	Field     string  `json:"field"`
	Amount    *string `json:"amount_rubles"`
	Category  *string `json:"category"`
	Date      *string `json:"spent_at"`
	Comment   *string `json:"comment"`
	ID        string  `json:"id"`
	Expected  *string `json:"expected_amount_rubles"`
	From      string  `json:"date_from"`
	To        string  `json:"date_to"`
	Search    string  `json:"search"`
	Limit     int     `json:"limit"`
	Selection string  `json:"selection"`
}

func strictArgs(call ToolCall) (arguments, error) {
	var p arguments
	var shape map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.Function.Arguments), &shape); err != nil || shape == nil {
		return p, expense.Invalid("аргументы должны быть объектом")
	}
	var schema map[string]any
	for _, t := range Tools() {
		if t.Function.Name == call.Function.Name {
			schema = t.Function.Parameters
		}
	}
	if schema == nil {
		return p, expense.Invalid("неизвестный инструмент")
	}
	properties := schema["properties"].(map[string]any)
	for key, v := range shape {
		if _, ok := properties[key]; !ok {
			return p, expense.Invalid("неизвестный аргумент " + key)
		}
		if string(v) == "null" {
			return p, expense.Invalid("null не поддерживается")
		}
	}
	for _, key := range schema["required"].([]string) {
		if _, ok := shape[key]; !ok {
			return p, expense.Invalid("отсутствует " + key)
		}
	}
	d := json.NewDecoder(bytes.NewBufferString(call.Function.Arguments))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, expense.Invalid("неверный тип аргумента")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, expense.Invalid("лишний JSON")
	}
	for _, v := range []string{p.Field, p.ID, p.From, p.To, p.Search, p.Selection} {
		if err := expense.Text(v); err != nil {
			return p, err
		}
	}
	for _, v := range []*string{p.Amount, p.Category, p.Date, p.Comment, p.Expected} {
		if v != nil {
			if err := expense.Text(*v); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}
func (a *Agent) executeAuthorized(ctx context.Context, user, update int64, call ToolCall, text string, s *Session, selected int64, mutated *bool, permission requestPermissions) (json.RawMessage, error) {
	if user <= 0 {
		return nil, expense.ErrNotFound
	}
	if multipleExpenseIDs(text) {
		return nil, expense.Invalid("укажите только один расход за один запрос")
	}
	p, err := strictArgs(call)
	if err != nil {
		return nil, err
	}
	name := call.Function.Name
	if err := permission.authorize(name, p); err != nil {
		return nil, err
	}
	if name == "clarify_request" {
		question, err := clarification(p.Field)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"question": question})
	}
	if name == "summarize_expenses" && listRequest.MatchString(text) {
		return nil, expense.Invalid("пользователь просит список расходов: вызови list_expenses с указанными периодом и категорией, а не summarize_expenses")
	}
	if name == "list_expenses" || name == "summarize_expenses" {
		if p.Limit < 0 || p.Limit > 10 {
			return nil, expense.Invalid("limit должен быть от 1 до 10")
		}
		if p.Limit == 0 {
			p.Limit = 10
		}
		if p.Selection != "" && p.Selection != "by_date" && p.Selection != "latest_created" {
			return nil, expense.Invalid("неверная сортировка")
		}
		if selected != 0 {
			return nil, expense.Invalid("расход уже выбран; используйте выбранный ID без нового поиска")
		}
		if p.Selection == "latest_created" && !latestRequested(text) {
			return nil, expense.Invalid("последний расход можно выбрать только по явному запросу пользователя; выполните обычный поиск")
		}
		f := expense.Filter{UserID: user, From: p.From, To: p.To, Search: p.Search, Limit: p.Limit, Latest: p.Selection == "latest_created"}
		if id, explicit := requestedExpenseID(text); explicit {
			if id == 0 {
				return nil, expense.ErrNotFound
			}
			f.ID = id
		}
		if p.Category != nil {
			f.Category = *p.Category
		}
		if err = f.Validate(); err != nil {
			return nil, err
		}
		page, e := a.Store.List(ctx, f)
		if e != nil {
			return nil, e
		}
		if name == "summarize_expenses" {
			return json.Marshal(map[string]any{"total": page.Total, "total_amount": strconv.FormatInt(page.TotalAmount, 10)})
		}
		s.LatestID = 0
		s.Choice = nil
		s.Candidates = page.Items
		s.CandidateTotal = page.Total
		if f.Latest && f.Limit == 1 && len(page.Items) == 1 {
			s.LatestID = page.Items[0].ID
		}
		return json.Marshal(expensewire.FromPage(page))
	}
	if *mutated {
		return nil, expense.Invalid("допустима одна операция изменения на сообщение")
	}
	var amount *int64
	if p.Amount != nil {
		n, e := expense.ParseRubles(*p.Amount)
		if e != nil {
			return nil, e
		}
		amount = &n
	}
	i := expense.Input{UserID: user}
	patch := expense.Patch{Amount: amount, Category: p.Category, SpentAt: p.Date, Comment: p.Comment}
	var id int64
	if name == "create_expense" {
		if amount == nil || p.Category == nil || p.Date == nil {
			return nil, expense.Invalid("нужны сумма, категория и дата")
		}
		i.Amount = *amount
		i.Category = *p.Category
		i.SpentAt = *p.Date
		if p.Comment != nil {
			i.Comment = *p.Comment
		}
		if err = i.Validate(); err != nil {
			return nil, err
		}
	} else {
		id, err = strconv.ParseInt(p.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, expense.Invalid("неверный ID")
		}
		if requested, explicit := requestedExpenseID(text); explicit && requested != id {
			return nil, expense.Invalid("ID не совпадает с явно указанным расходом")
		}
		var candidate *expense.Expense
		for idx := range s.Candidates {
			if s.Candidates[idx].ID == id {
				candidate = &s.Candidates[idx]
			}
		}
		if candidate == nil {
			return nil, expense.Invalid("сначала найдите собственный расход")
		}

		expected := p.Expected
		if token, ok := previousAmount(text); ok {
			expected = &token
		}
		if expected != nil {
			n, e := expense.ParseRubles(*expected)
			if e != nil {
				return nil, e
			}
			patch.ExpectedAmount = &n
			if (s.CandidateTotal <= 1 || selected == id || s.LatestID == id) && n != candidate.Amount {
				return nil, expense.Invalid("прежняя сумма не совпадает с найденным расходом. Уточните, какой расход изменить.")
			}
		}

		if name == "update_expense" {
			if err = patch.Validate(); err != nil {
				return nil, err
			}
		}
		if selected != 0 && selected != id {
			return nil, expense.Invalid("ID не совпадает с выбором пользователя")
		}
		if s.CandidateTotal > 1 && selected != id && s.LatestID != id {
			s.Pending = &PendingAction{Kind: name, Patch: patch, ExpectedAmount: patch.ExpectedAmount, IntentValidated: true}
			return nil, expense.Invalid("выберите расход из списка")
		}
	}
	result, err := a.executeMutation(ctx, update, user, name, id, i, patch)
	if err == nil {
		*mutated = true
		clearSelection(s)
	}
	return result, err
}

func (a *Agent) executeMutation(ctx context.Context, update, owner int64, name string, id int64, input expense.Input, patch expense.Patch) (json.RawMessage, error) {
	var operation expense.Operation
	switch name {
	case "create_expense":
		operation = expense.Create
	case "update_expense":
		operation = expense.Update
	case "delete_expense":
		operation = expense.Delete
	default:
		return nil, expense.Invalid("неизвестная операция")
	}
	return a.Store.Execute(ctx, mutationKey(update), owner, expense.Command{Operation: operation, ID: id, Input: input, Patch: patch})
}
