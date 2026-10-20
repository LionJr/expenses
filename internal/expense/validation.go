package expense

import (
	"strings"
	"time"
	"unicode/utf8"
)

// PostgreSQL TEXT and JSONB cannot represent U+0000. Reject it before any SQL
// mutation so a permanent input error cannot become a retryable bot update.
func Text(s string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return Invalid("текст содержит недопустимый символ")
	}
	return nil
}
func Date(s string) error {
	if d, err := time.Parse("2006-01-02", s); err != nil || d.Year() < 1 {
		return Invalid("дата должна иметь формат YYYY-MM-DD")
	}
	return nil
}
