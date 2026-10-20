package expense

import "unicode/utf8"

type Input struct {
	UserID   int64
	Amount   int64
	Category string
	SpentAt  string
	Comment  string
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
