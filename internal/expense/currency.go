package expense

import (
	"fmt"
	"regexp"
	"strings"
)

// All supported currencies have two decimal places. The database and public
// API keep integer minor units; selecting a currency never converts amounts.
type Currency string

const (
	TMT Currency = "TMT"
	RUB Currency = "RUB"
	USD Currency = "USD"
	EUR Currency = "EUR"
	AZN Currency = "AZN"
)

type currencyInfo struct {
	name    string
	aliases string
	entry   *regexp.Regexp
	suffix  *regexp.Regexp
	label   *regexp.Regexp
}

var currencies = map[Currency]currencyInfo{
	TMT: {name: "Туркменский манат", aliases: `TMT|манат(?:а|ов)?|manat|ман\.?`},
	RUB: {name: "Российский рубль", aliases: `RUB|₽|руб(?:лей|ля|ль)?\.?`},
	USD: {name: "Доллар США", aliases: `USD|\$|доллар(?:а|ов)?`},
	EUR: {name: "Евро", aliases: `EUR|€|евро`},
	AZN: {name: "Азербайджанский манат", aliases: `AZN|₼|манат(?:а|ов)?|manat|ман\.?`},
}

var labelledAmount *regexp.Regexp
var prefixedAmount *regexp.Regexp

func init() {
	var labels []string
	for _, code := range []Currency{TMT, RUB, USD, EUR, AZN} {
		info := currencies[code]
		info.entry = regexp.MustCompile(`(?i)^\s*[\p{L}][\p{L}\s]*\s+(?:(?:` + info.aliases + `)\s*)?([-−]?[0-9]+(?:[.,][0-9]+)?)(?:\s*(?:` + info.aliases + `))?(?:\s+(?:сегодня|вчера))?\s*$`)
		info.suffix = regexp.MustCompile(`(?i)\s*(?:` + info.aliases + `)$`)
		info.label = regexp.MustCompile(`(?i)^(?:` + info.aliases + `)$`)
		currencies[code] = info
		labels = append(labels, info.aliases)
	}
	labelledAmount = regexp.MustCompile(`(?i)[0-9]+(?:[.,][0-9]+)?\s*(` + strings.Join(labels, "|") + `)(?:$|[^\p{L}])`)
	prefixedAmount = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(` + strings.Join(labels, "|") + `)\s*[-−]?[0-9]+(?:[.,][0-9]+)?`)
}

func ParseCurrency(value string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(value)))
	if _, ok := currencies[c]; !ok {
		return "", fmt.Errorf("APP_CURRENCY must be TMT, RUB, USD, EUR or AZN")
	}
	return c, nil
}

// Zero values in programmatic callers use the same default as config.Load.
func (c Currency) Code() string {
	if c == "" {
		return string(TMT)
	}
	return string(c)
}

func (c Currency) info() currencyInfo { return currencies[Currency(c.Code())] }
func (c Currency) Name() string       { return c.info().name }

func (c Currency) FormatAmount(amount int64) string {
	return fmt.Sprintf("%d,%02d %s", amount/100, amount%100, c.Code())
}

func (c Currency) EntryAmount(text string) (string, bool) {
	match := c.info().entry.FindStringSubmatch(text)
	if len(match) < 2 {
		return "", false
	}
	return match[1], true
}

func (c Currency) StripSuffix(text string) string {
	return strings.TrimSpace(c.info().suffix.ReplaceAllString(strings.TrimSpace(text), ""))
}

// Generic "манат" is interpreted in the selected TMT/AZN currency. Explicit
// foreign codes/symbols must not silently become a local-currency expense.
func (c Currency) HasForeignAmount(text string) bool {
	for _, match := range labelledAmount.FindAllStringSubmatch(text, -1) {
		if !c.info().label.MatchString(match[1]) {
			return true
		}
	}
	for _, match := range prefixedAmount.FindAllStringSubmatch(text, -1) {
		if !c.info().label.MatchString(match[1]) {
			return true
		}
	}
	return false
}
