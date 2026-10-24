package expense

import "testing"

func TestCurrenciesUseExactMinorUnitsAndOwnLabels(t *testing.T) {
	for _, tc := range []struct {
		code  Currency
		label string
	}{
		{TMT, "манатов"}, {RUB, "рублей"}, {USD, "долларов"}, {EUR, "евро"}, {AZN, "₼"},
	} {
		t.Run(tc.code.Code(), func(t *testing.T) {
			c, err := ParseCurrency(" " + tc.code.Code() + " ")
			if err != nil || c != tc.code || c.Name() == "" {
				t.Fatal(c, err)
			}
			text := "кофе 12,34 " + tc.label
			amount, ok := c.EntryAmount(text)
			n, err := ParseAmount(amount)
			if !ok || err != nil || n != 1234 || c.FormatAmount(n) != "12,34 "+c.Code() || c.HasForeignAmount(text) {
				t.Fatalf("wrong currency/units: %q %d %v", text, n, err)
			}
			if got := c.StripSuffix("12,34 " + tc.label); got != "12,34" {
				t.Fatal(got)
			}
			if _, ok := c.EntryAmount("кофе " + tc.code.Code() + "12.34"); !ok {
				t.Fatal("currency prefix rejected")
			}
			for _, other := range []Currency{TMT, RUB, USD, EUR, AZN} {
				if foreign := c.HasForeignAmount("добавь кофе 12.34 " + other.Code()); foreign != (c != other) {
					t.Fatalf("%s accepted foreign amount in %s", c, other)
				}
				if foreign := c.HasForeignAmount("добавь кофе " + other.Code() + "12.34"); foreign != (c != other) {
					t.Fatalf("%s accepted foreign prefix %s", c, other)
				}
			}
		})
	}
	if c, err := ParseCurrency(" usd "); err != nil || c != USD {
		t.Fatal(c, err)
	}
	for _, invalid := range []string{"", "JPY", "KWD", "TMT USD", "<script>"} {
		if _, err := ParseCurrency(invalid); err == nil {
			t.Fatalf("accepted currency %q", invalid)
		}
	}
	if (Currency("")).Code() != "TMT" || (Currency("")).FormatAmount(1) != "0,01 TMT" {
		t.Fatal("default currency differs from configuration default")
	}
}
