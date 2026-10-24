package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A small, local blocklist catches obvious defaults, not every breached password.
// Operators should generate a unique random password in a password manager.
var commonPanelPasswords = map[string]bool{
	"passwordpassword":             true,
	"password123456789":            true,
	"password1234567890":           true,
	"123456789012345":              true,
	"1234567890123456":             true,
	"123456789123456789":           true,
	"qwertyuiopasdfgh":             true,
	"qwertyuiopasdfghjkl":          true,
	"administrator123":             true,
	"familyexpenses123":            true,
	"family-expenses":              true,
	"changemechangeme":             true,
	"letmeinletmeinletmein":        true,
	"correct horse battery staple": true,
}

func ValidatePanelPassword(username, password string) error {
	if strings.TrimSpace(username) == "" || utf8.RuneCountInString(username) > 128 || strings.ContainsRune(username, ':') || strings.ContainsFunc(username, unicode.IsControl) {
		return fmt.Errorf("Некорректный логин")
	}
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 15 || utf8.RuneCountInString(password) > 256 {
		return fmt.Errorf("Пароль должен содержать 15–256 символов; используйте уникальный пароль из менеджера паролей")
	}
	if strings.TrimSpace(password) == "" || strings.EqualFold(password, username) || commonPanelPasswords[strings.ToLower(strings.TrimSpace(password))] || repeatedPattern(password) {
		return fmt.Errorf("Пароль слишком предсказуем; создайте уникальный случайный пароль")
	}
	return nil
}

func repeatedPattern(value string) bool {
	runes := []rune(value)
	for size := 1; size <= 4 && size < len(runes); size++ {
		repeated := true
		for i := size; i < len(runes); i++ {
			if runes[i] != runes[i%size] {
				repeated = false
				break
			}
		}
		if repeated {
			return true
		}
	}
	return false
}
