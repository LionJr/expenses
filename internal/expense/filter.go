package expense

import "unicode/utf8"

type Filter struct {
	ID                         int64
	UserID                     int64
	Category, From, To, Search string
	Limit, Offset              int
	Latest                     bool
}

// Normalize returns a copy with the default page size applied.
func (f Filter) Normalize() Filter {
	if f.Limit == 0 {
		f.Limit = 20
	}
	return f
}

func (f Filter) Validate() error {
	if f.UserID < 0 || f.Offset < 0 || f.Limit < 0 || f.Limit > 100 {
		return Invalid("неверная пагинация или участник")
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
