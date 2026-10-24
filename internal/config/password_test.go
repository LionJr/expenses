package config

import (
	"strings"
	"testing"
)

func TestPanelPasswordPolicy(t *testing.T) {
	for _, test := range []struct {
		name, username, password string
		valid                    bool
	}{
		{"empty credentials", "", "", false},
		{"short", "family", "aB7!small", false},
		{"common", "family", "Password123456789", false},
		{"repeated", "family", strings.Repeat("abcd", 8), false},
		{"one character", "family", strings.Repeat("я", 32), false},
		{"blank", "family", strings.Repeat(" ", 20), false},
		{"same as username", "long-family-name", "long-family-name", false},
		{"username colon", "family:admin", "An unusual family passphrase", false},
		{"username control", "family\n", "An unusual family passphrase", false},
		{"too long", "family", strings.Repeat("long", 65), false},
		{"passphrase without symbols", "family", "Several unusual words for our household", true},
		{"random", "family", "8d6c1e59b430af72e0c9d3b1265f8a07", true},
		{"unicode and spaces", "семья", "Наша фраза с разными словами", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePanelPassword(test.username, test.password)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if err != nil && test.password != "" && strings.Contains(err.Error(), test.password) {
				t.Fatal("password leaked in validation error")
			}
		})
	}
}
