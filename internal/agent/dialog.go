package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

type PendingAction struct {
	IntentValidated bool          `json:"intent_validated,omitempty"`
	Kind            string        `json:"kind"`
	Patch           expense.Patch `json:"patch"`
	ExpectedAmount  *int64        `json:"expected_amount,string,omitempty"`
}
type Session struct {
	Request        *RequestDraft     `json:"request,omitempty"`
	Edit           *EditDraft        `json:"edit,omitempty"`
	Choice         *botstate.Choice  `json:"choice,omitempty"`
	History        []Message         `json:"history"`
	Candidates     []expense.Expense `json:"candidates"`
	CandidateTotal int64             `json:"candidate_total"`
	Pending        *PendingAction    `json:"pending,omitempty"`
	LatestID       int64             `json:"-"`
}

type RequestDraft struct {
	Text        string             `json:"text"`
	Field       string             `json:"field"`
	Permissions requestPermissions `json:"permissions"`
}

type EditDraft struct {
	Search string `json:"search"`
	Field  string `json:"field,omitempty"`
}

func selectCandidate(text string, c []expense.Expense) int64 {
	t := strings.ToLower(strings.Trim(strings.TrimSpace(text), ".!?"))
	ord := map[string]int{"первую": 0, "первый": 0, "1": 0, "вторую": 1, "второй": 1, "2": 1, "третью": 2, "третий": 2, "3": 2, "четвертую": 3, "четвёртую": 3, "4": 3, "пятую": 4, "5": 4, "шестую": 5, "6": 5, "седьмую": 6, "7": 6, "восьмую": 7, "8": 7, "девятую": 8, "9": 8, "десятую": 9, "10": 9}
	if n, ok := ord[t]; ok && n < len(c) {
		return c[n].ID
	}
	for _, e := range c {
		if t == "id "+strconv.FormatInt(e.ID, 10) || t == "#"+strconv.FormatInt(e.ID, 10) {
			return e.ID
		}
	}
	return 0
}

func clearSelection(s *Session) {
	s.Request = nil
	s.Edit = nil
	s.Choice = nil
	s.Candidates = nil
	s.CandidateTotal = 0
	s.Pending = nil
	s.LatestID = 0
}

func cancelReply(text string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(text), ".!?")), " "))
	switch normalized {
	case "отмена", "отменить", "отмени", "нет", "не надо", "не нужно", "не хочу", "передумал", "передумала", "стоп", "не удаляй", "не меняй", "no", "nope", "cancel", "stop":
		return true
	}
	return false
}
func remember(s *Session, text, answer string) {
	s.History = append(s.History, Message{Role: "user", Content: safeText(text)}, Message{Role: "assistant", Content: truncate(safeText(answer), 3500)})
	for len(s.History) > 30 || historySize(s.History) > 24000 {
		if len(s.History) < 2 {
			s.History = nil
			break
		}
		s.History = s.History[2:]
	}
}

func safeText(text string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\x00", "�")
}

func previousAmount(text string) (string, bool) {
	if match := previousAmountClause.FindStringSubmatch(text); len(match) > 1 {
		return strings.TrimSpace(amountCurrency.ReplaceAllString(strings.TrimSpace(match[1]), "")), true
	}
	match := oldAmount.FindStringSubmatchIndex(text)
	if len(match) < 4 {
		return "", false
	}
	token := text[match[2]:match[3]]
	// In "не 350, а 450" the last comma is punctuation, not a decimal
	// separator. Keep all other punctuation/digits so ParseRubles rejects the
	// entire malformed token rather than silently accepting a valid prefix.
	if strings.HasSuffix(token, ",") && match[3] < len(text) {
		r, _ := utf8.DecodeRuneInString(text[match[3]:])
		if unicode.IsSpace(r) {
			token = strings.TrimSuffix(token, ",")
		}
	}
	return token, true
}

const (
	latestObject  = `(?:последний расход|последнюю трату|последняя трата|последнюю поездку|последнее такси|последнее|последнюю|последний|самый новый расход|самую новую трату|самую новую поездку)`
	decimalAmount = `[0-9]+(?:[.,][0-9]{1,2})?`
)

var (
	previousAmountClause = regexp.MustCompile(`(?i)(?:^|[^\p{L}])не\s+(.+?)(?:,\s*|\s+)а\s+`)
	amountCurrency       = regexp.MustCompile(`(?i)\s*(?:₽|руб(?:лей|ля|ль)?\.?)$`)

	// Only whole, unqualified requests authorize the latest shortcut. Arbitrary
	// mentions of "last" (including exclusions and negation) must not grant it.
	latestRequest      = regexp.MustCompile(`^(?:(?:пожалуйста,? )?(?:удали|удалить|убери|покажи|найди) )?` + latestObject + `(?:,? пожалуйста)?$`)
	latestCorrection   = regexp.MustCompile(`^(?:последнее было|последняя трата была|последний расход был) не ` + decimalAmount + `,? а ` + decimalAmount + `$`)
	latestAmountChange = regexp.MustCompile(`^(?:измени|исправь) ` + latestObject + ` на ` + decimalAmount + `$`)
	selectionWord      = regexp.MustCompile(`(?i)^(?:[0-9]+|первую|первый|вторую|второй|третью|третий|четв[её]ртую|пятую|шестую|седьмую|восьмую|девятую|десятую|#\d+|id \d+)$`)

	// Only an unqualified, single-word search target is handled without a model.
	simpleDelete        = regexp.MustCompile(`(?i)^\s*(?:удали|удалить|убери)\s+([\p{L}]{3,})[.!?]?\s*$`)
	simpleUpdate        = regexp.MustCompile(`(?i)^\s*(?:измени|изменить|исправь)\s+([\p{L}]{3,})(?:\s+на\s+([0-9]+(?:[.,][0-9]+)?))?[.!?]?\s*$`)
	simpleList          = regexp.MustCompile(`(?i)^\s*(?:покажи|показать|покажите)\s+(?:мои\s+)?(?:расходы|траты)\s+(?:за|на)\s+([\p{L}]{3,})[.!?]?\s*$`)
	amountReply         = regexp.MustCompile(`^[-−]?[0-9]+(?:[.,][0-9]+)?$`)
	explicitProhibition = regexp.MustCompile(`(?i)^\s*(?:пожалуйста,?\s+)?не\s+(?:удаляй|удалять|меняй|изменяй|изменять|трогай)\s+[\p{L}0-9# -]+[.!?]?\s*$`)
)

// Only known expense targets can bypass interpretation. An arbitrary word after
// "за" may be a period (e.g. "октябрь"), not a comment substring.
func simpleListSearch(text string) string {
	m := simpleList.FindStringSubmatch(text)
	if len(m) == 0 {
		return ""
	}
	target := strings.ToLower(m[1])
	if expense.Category(target) || target == "такси" || target == "кофе" {
		return target
	}
	return ""
}
func clarification(field string) (string, error) {
	switch field {
	case "amount":
		return "Укажите сумму расхода в рублях, например «кофе 350,50».", nil
	case "date":
		return "Уточните дату расхода или период, например «вчера» или «за эту неделю».", nil
	case "category":
		return "Укажите категорию: еда, транспорт, жильё, развлечения, здоровье, покупки или другое.", nil
	case "comment":
		return "Укажите новый комментарий и расход, который нужно изменить.", nil
	case "expense":
		return "Уточните, какой расход нужно найти или изменить: укажите описание или #ID.", nil
	case "single_expense":
		return "Пришлите каждый расход отдельным сообщением, например «кофе 350».", nil
	case "request":
		return "Уточните запрос: например, «кофе 350», «покажи траты за неделю» или «измени такси на 500».", nil
	default:
		return "", expense.Invalid("неизвестное поле уточнения")
	}
}
func simpleDeleteSearch(text string) string {
	m := simpleDelete.FindStringSubmatch(text)
	if len(m) == 0 || latestRequested(text) {
		return ""
	}
	target := strings.ToLower(m[1])
	aliases := map[string]string{"аренду": "аренда", "стоматолога": "стоматолог", "покупку": "покупк", "покупки": "покупк", "книгу": "книг", "книга": "книг", "лекарство": "лекарств", "лекарства": "лекарств"}
	if canonical, ok := aliases[target]; ok {
		return canonical
	}
	if expense.Category(target) {
		return target
	}
	switch target {
	case "такси", "кофе", "аренда", "стоматолог", "обед", "ужин", "завтрак", "бассейн", "покупк", "книг", "лекарств":
		return target
	}
	return ""
}
func selectionReply(text string) bool {
	return selectionWord.MatchString(strings.Trim(strings.TrimSpace(text), ".!?"))
}
func choicePrompt(s Session) string {
	var b strings.Builder
	if s.Pending != nil {
		if s.Pending.Kind == toolDeleteExpense {
			b.WriteString("Какой расход удалить?\n")
		} else {
			b.WriteString("Какой расход изменить?\n")
		}
		b.WriteString("Ответьте номером («2», «вторую») или #ID. Отменить — «отмена».\n")
	} else {
		fmt.Fprintf(&b, "Найдено расходов: %d.\n", s.CandidateTotal)
	}
	b.WriteByte('\n')
	for i, e := range s.Candidates {
		fmt.Fprintf(&b, "%d. %d,%02d ₽ · %s · %s", i+1, e.Amount/100, e.Amount%100, e.SpentAt, e.Category)
		if comment := strings.TrimSpace(strings.ReplaceAll(truncate(e.Comment, 60), "\n", " ")); comment != "" {
			fmt.Fprintf(&b, " · %s", comment)
		}
		fmt.Fprintf(&b, " (#%d)\n", e.ID)
	}
	if s.CandidateTotal > int64(len(s.Candidates)) {
		b.WriteString("Показана часть расходов. Можно уточнить поиск новым сообщением.")
	}
	return strings.TrimSpace(b.String())
}
func latestRequested(text string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.Trim(strings.TrimSpace(text), ".!?"))), " ")
	return latestRequest.MatchString(normalized) || latestCorrection.MatchString(normalized) || latestAmountChange.MatchString(normalized)
}
func prepareChoice(s *Session, update int64) {
	s.Choice = &botstate.Choice{UpdateID: update, State: botstate.AwaitingDelivery}
}

// IsSelectionReply lets the transport reject choices sent before their prompt.
func IsSelectionReply(text string) bool { return selectionReply(text) }
