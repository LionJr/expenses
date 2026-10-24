package agent

import (
	"regexp"
	"strings"

	"github.com/LionJr/expenses/internal/expense"
)

// Each bit permits updating one field: combine with |, test with &.
const (
	fieldAmount uint8 = 1 << iota
	fieldDate
	fieldCategory
	fieldComment
	allFields = fieldAmount | fieldDate | fieldCategory | fieldComment
)

// Permissions are derived by application code from the current request, never
// from model arguments or old conversation history. Unknown intent cannot write.
type requestPermissions struct {
	Mutation string `json:"mutation,omitempty"`
	Fields   uint8  `json:"fields,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

var (
	readIntent        = regexp.MustCompile(`(?i)^\s*(?:пожалуйста[,\s]+)?(?:покажи|покажите|показать|найди|найдите|найти|сколько|посчитай|посчитайте|отчет|отчёт|список|как|что|почему|расскажи|объясни)(?:\s|$)`)
	conditionalIntent = regexp.MustCompile(`(?i)^\s*(?:если|когда|например)(?:\s|$)`)
	negatedIntent     = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:не|нельзя)\s+(?:(?:хочу|надо|нужно|буду)\s+)?(?:удал|убир|меня|измен|исправ|добав|запис|созда|куп|покуп|оплат|потрат|заплат)[\p{L}]*`)
	deleteIntent      = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:удали|удалите|удалить|убери|уберите|убрать)(?:[^\p{L}]|$)`)
	updateIntent      = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:измени|измените|изменить|исправь|исправьте|исправить|поменяй|поменяйте|обнови)(?:[^\p{L}]|$)`)
	createIntent      = regexp.MustCompile(`(?i)^\s*(?:пожалуйста[,\s]+)?(?:я\s+)?(?:добавь|добавьте|добавить|запиши|запишите|создай|создайте|купил|купила|оплатил|оплатила|потратил|потратила|заплатил|заплатила)(?:\s|$)`)
	correctionIntent  = regexp.MustCompile(`(?i)(?:^|\s)(?:было|была|был)\s+не\s+`)
	changeToAmount    = regexp.MustCompile(`(?i)\sна\s+[-−]?[0-9]`)
	simpleDescription = regexp.MustCompile(`^[\p{L}][\p{L}\s]{0,199}$`)
)

func permissionsFor(c expense.Currency, text string) requestPermissions {
	if conditionalIntent.MatchString(text) || negatedIntent.MatchString(text) {
		return requestPermissions{ReadOnly: true}
	}
	if readIntent.MatchString(text) {
		return requestPermissions{ReadOnly: true}
	}
	del, upd, add := deleteIntent.MatchString(text), updateIntent.MatchString(text) || correctionIntent.MatchString(text), createIntent.MatchString(text)
	count := 0
	for _, v := range []bool{del, upd, add} {
		if v {
			count++
		}
	}
	if count > 1 {
		return requestPermissions{}
	}
	if del {
		return requestPermissions{Mutation: toolDeleteExpense}
	}
	if upd {
		fields := uint8(0)
		prefix := strings.SplitN(strings.Join(strings.Fields(strings.ToLower(text)), " "), " на ", 2)[0]
		for _, word := range strings.FieldsFunc(prefix, func(r rune) bool { return !(r >= 'а' && r <= 'я') && r != 'ё' }) {
			switch word {
			case "сумма", "сумму", "суммы", "сумме", "суммой":
				fields |= fieldAmount
			case "дата", "дату", "даты", "дате", "датой":
				fields |= fieldDate
			case "категория", "категорию", "категории", "категорией":
				fields |= fieldCategory
			case "комментарий", "комментария", "комментарии", "комментарию", "комментарием":
				fields |= fieldComment
			}
		}
		if fields == 0 && (correctionIntent.MatchString(text) || changeToAmount.MatchString(text)) {
			fields = fieldAmount
		}
		if fields == 0 {
			fields = allFields
		}
		return requestPermissions{Mutation: toolUpdateExpense, Fields: fields}
	}
	_, compactEntry := c.EntryAmount(text)
	if add || compactEntry {
		return requestPermissions{Mutation: toolCreateExpense}
	}
	return requestPermissions{}
}

func (p requestPermissions) authorize(name string, args arguments) error {
	if !isMutation(name) {
		return nil
	}
	if p.Mutation != name || p.ReadOnly {
		return expense.Invalid("запрос не разрешает эту операцию; уточните намерение пользователя")
	}
	if name == toolUpdateExpense && ((args.Amount != nil && p.Fields&fieldAmount == 0) || (args.Date != nil && p.Fields&fieldDate == 0) || (args.Category != nil && p.Fields&fieldCategory == 0) || (args.Comment != nil && p.Fields&fieldComment == 0)) {
		return expense.Invalid("запрос не разрешает изменение этих полей")
	}
	return nil
}

func permittedTools(c expense.Currency, p requestPermissions) []Tool {
	var result []Tool
	for _, t := range Tools(c) {
		if isMutation(t.Function.Name) && (p.ReadOnly || t.Function.Name != p.Mutation) {
			continue
		}
		if t.Function.Name == toolUpdateExpense {
			properties := t.Function.Parameters["properties"].(map[string]any)
			for key, flag := range map[string]uint8{"amount_decimal": fieldAmount, "spent_at": fieldDate, "category": fieldCategory, "comment": fieldComment} {
				if p.Fields&flag == 0 {
					delete(properties, key)
				}
			}
		}
		result = append(result, t)
	}
	return result
}
