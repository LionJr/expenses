package expense

import (
	"strconv"
	"strings"
)

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
