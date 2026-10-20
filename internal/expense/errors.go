package expense

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid  = errors.New("некорректные данные")
	ErrNotFound = errors.New("расход не найден")
	ErrConflict = errors.New("расход изменён другим запросом; обновите данные и повторите правку")
)

func Invalid(s string) error { return fmt.Errorf("%w: %s", ErrInvalid, s) }
