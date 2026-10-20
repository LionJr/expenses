package expense

import "unicode/utf8"

type Patch struct {
	ExpectedVersion *int64
	ExpectedAmount  *int64
	Amount          *int64
	Category        *string
	SpentAt         *string
	Comment         *string
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
