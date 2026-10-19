package expense

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("некорректные данные")
	ErrNotFound = errors.New("расход не найден")
	ErrConflict = errors.New("расход изменён другим запросом; обновите данные и повторите правку")

	Categories = []string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"}
)

type User struct {
	ID       int64  `json:"id,string"`
	Name     string `json:"name"`
	Username string `json:"username"`
}
type Expense struct {
	Version   int64     `json:"version,string"`
	ID        int64     `json:"id,string"`
	UserID    int64     `json:"user_id,string"`
	Amount    int64     `json:"amount,string"`
	Category  string    `json:"category"`
	SpentAt   string    `json:"spent_at"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Input struct {
	UserID   int64  `json:"user_id,string"`
	Amount   int64  `json:"amount,string"`
	Category string `json:"category"`
	SpentAt  string `json:"spent_at"`
	Comment  string `json:"comment"`
}
type Patch struct {
	ExpectedVersion *int64  `json:"expected_version,string,omitempty"`
	ExpectedAmount  *int64  `json:"-"`
	Amount          *int64  `json:"amount,string"`
	Category        *string `json:"category"`
	SpentAt         *string `json:"spent_at"`
	Comment         *string `json:"comment"`
}
type Filter struct {
	ID                         int64
	UserID                     int64
	Category, From, To, Search string
	Limit, Offset              int
	Latest                     bool
}
type Page struct {
	Items       []Expense `json:"items"`
	Total       int64     `json:"total"`
	TotalAmount int64     `json:"total_amount,string"`
}

func Invalid(s string) error { return fmt.Errorf("%w: %s", ErrInvalid, s) }

// PostgreSQL TEXT and JSONB cannot represent U+0000. Reject it before any SQL
// mutation so a permanent input error cannot become a retryable bot update.
func Text(s string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return Invalid("текст содержит недопустимый символ")
	}
	return nil
}
func Category(s string) bool {
	for _, c := range Categories {
		if c == s {
			return true
		}
	}
	return false
}
func Date(s string) error {
	if d, err := time.Parse("2006-01-02", s); err != nil || d.Year() < 1 {
		return Invalid("дата должна иметь формат YYYY-MM-DD")
	}
	return nil
}
func (i Input) Validate() error {
	if i.UserID <= 0 {
		return Invalid("выберите участника")
	}
	if i.Amount <= 0 {
		return Invalid("сумма должна быть положительной")
	}
	if !Category(i.Category) {
		return Invalid("неизвестная категория")
	}
	if err := Date(i.SpentAt); err != nil {
		return err
	}
	if utf8.RuneCountInString(i.Comment) > 1000 {
		return Invalid("комментарий длиннее 1000 символов")
	}
	return Text(i.Comment)
}
func (p Patch) Validate() error {
	if p.ExpectedVersion != nil && *p.ExpectedVersion <= 0 {
		return Invalid("неверная версия расхода")
	}
	if p.Amount == nil && p.Category == nil && p.SpentAt == nil && p.Comment == nil {
		return Invalid("нет полей для изменения")
	}
	if p.Amount != nil && *p.Amount <= 0 {
		return Invalid("сумма должна быть положительной")
	}
	if p.Category != nil && !Category(*p.Category) {
		return Invalid("неизвестная категория")
	}
	if p.SpentAt != nil {
		if err := Date(*p.SpentAt); err != nil {
			return err
		}
	}
	if p.Comment != nil && utf8.RuneCountInString(*p.Comment) > 1000 {
		return Invalid("комментарий длиннее 1000 символов")
	}
	if p.Comment != nil {
		return Text(*p.Comment)
	}
	return nil
}
func (f *Filter) Validate() error {
	if f.UserID < 0 || f.Offset < 0 || f.Limit < 0 || f.Limit > 100 {
		return Invalid("неверная пагинация или участник")
	}
	if f.Limit == 0 {
		f.Limit = 20
	}
	if f.Category != "" && !Category(f.Category) {
		return Invalid("неизвестная категория")
	}
	for _, d := range []string{f.From, f.To} {
		if d != "" {
			if err := Date(d); err != nil {
				return err
			}
		}
	}
	if f.From != "" && f.To != "" && f.From > f.To {
		return Invalid("начало периода позже окончания")
	}
	if utf8.RuneCountInString(f.Search) > 200 {
		return Invalid("слишком длинный поиск")
	}
	return Text(f.Search)
}

// ParseRubles uses decimal strings exclusively; no rounding or floating point.
func ParseRubles(s string) (int64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, Invalid("неверная сумма")
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, Invalid("нужно не более двух знаков после запятой")
		}
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	digits := parts[0] + fraction
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, Invalid("неверная сумма")
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, Invalid("сумма вне допустимого диапазона")
	}
	return n, nil
}
