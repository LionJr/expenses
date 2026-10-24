package telegram

import (
	"html"
	"regexp"
	"strings"
)

var replyHighlight = regexp.MustCompile(`\d+(?:[.,]\d{2})? (?:TMT|RUB|USD|EUR|AZN)|\d{4}[-‐‑–]\d{2}[-‐‑–]\d{2}|#\d+`)

// Stored replies stay plain text. Escape all provider/user text before adding
// our own Telegram HTML, including on retries of previously stored deliveries.
func formatReply(text string) string {
	var out strings.Builder
	previous := 0
	for _, match := range replyHighlight.FindAllStringIndex(text, -1) {
		out.WriteString(html.EscapeString(text[previous:match[0]]))
		value := text[match[0]:match[1]]
		if strings.Contains(value, " ") { // Money matches include a space before the currency code.
			out.WriteString("<b>" + html.EscapeString(value) + "</b>")
		} else {
			out.WriteString("<code>" + html.EscapeString(value) + "</code>")
		}
		previous = match[1]
	}
	out.WriteString(html.EscapeString(text[previous:]))
	return out.String()
}
