package expense

var categories = [...]string{"еда", "транспорт", "жильё", "развлечения", "здоровье", "покупки", "другое"}

// Categories returns a copy so callers cannot change the accepted categories.
func Categories() []string {
	return append([]string(nil), categories[:]...)
}

func Category(s string) bool {
	for _, c := range categories {
		if c == s {
			return true
		}
	}
	return false
}
