package telegram

import "testing"

func TestFormatReply(t *testing.T) {
	cases := []struct{ text, want string }{
		{"800,00 TMT · 2026-10-18 · Такси (#4)", "<b>800,00 TMT</b> · <code>2026-10-18</code> · Такси (<code>#4</code>)"},
		{"Итого: 2550,25 TMT.", "Итого: <b>2550,25 TMT</b>."},
		{"12,34 RUB / 12,34 USD / 12,34 EUR / 12,34 AZN", "<b>12,34 RUB</b> / <b>12,34 USD</b> / <b>12,34 EUR</b> / <b>12,34 AZN</b>"},
		{"Дата: 2026‑10‑19", "Дата: <code>2026‑10‑19</code>"},
		{"2026‐10‐19 / 2026–10–18", "<code>2026‐10‐19</code> / <code>2026–10–18</code>"},
		{"<b>кофе</b> & <script>bad</script>", "&lt;b&gt;кофе&lt;/b&gt; &amp; &lt;script&gt;bad&lt;/script&gt;"},
		{"Найдено расходов: 1.", "Найдено расходов: 1."},
		{"Кофе 'special'", "Кофе &#39;special&#39;"},
	}
	for _, c := range cases {
		if got := formatReply(c.text); got != c.want {
			t.Errorf("formatReply(%q) = %q; want %q", c.text, got, c.want)
		}
	}
}
