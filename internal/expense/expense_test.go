package expense

import (
	"errors"
	"math"
	"testing"
)

func TestParseRubles(t *testing.T) {
	cases := map[string]int64{"350": 35000, "350,50": 35050, "350.5": 35050, " 0.01 ": 1, "92233720368547758.07": math.MaxInt64, "0001.01": 101}
	for in, want := range cases {
		got, e := ParseRubles(in)
		if e != nil || got != want {
			t.Errorf("%q: got %d,%v want %d", in, got, e, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "+1", ".5", "1.", "1.001", "1e3", "NaN", "92233720368547758.08", "92233720368547759", "1 000", "１"} {
		if _, e := ParseRubles(in); e == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestTextValidation(t *testing.T) {
	for _, text := range []string{"bad\x00text", string([]byte{0xff})} {
		if !errors.Is(Text(text), ErrInvalid) {
			t.Fatalf("accepted invalid text %q", text)
		}
	}
	for _, text := range []string{"", "кофе\nс молоком", `literal \u0000`, "☕"} {
		if err := Text(text); err != nil {
			t.Fatal(err)
		}
	}
}
func TestValidation(t *testing.T) {
	for _, d := range []string{"2026-02-29", "2026-13-01", "17.10.2026", "2026-1-1"} {
		if Date(d) == nil {
			t.Errorf("accepted date %s", d)
		}
	}
	if e := Date("2024-02-29"); e != nil {
		t.Fatal(e)
	}
	f := Filter{From: "2026-10-17", To: "2026-10-16"}
	if f.Validate() == nil {
		t.Fatal("accepted reversed period")
	}
	if (Patch{}).Validate() == nil {
		t.Fatal("accepted empty patch")
	}
	empty := ""
	if e := (Patch{Comment: &empty}).Validate(); e != nil {
		t.Fatal(e)
	}
}
