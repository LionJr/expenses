package telegram

import "testing"

func TestFormatReply(t *testing.T) {
	cases := []struct{ text, want string }{
		{"800,00 ₽ · 2026-10-18 · Такси (#4)", "<b>800,00 ₽</b> · <code>2026-10-18</code> · Такси (<code>#4</code>)"},
		{"Итого: 2550,25 ₽.", "Итого: <b>2550,25 ₽</b>."},
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
