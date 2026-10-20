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

func TestFilterValidationDoesNotApplyDefaults(t *testing.T) {
	f := Filter{UserID: 42, Category: "еда", Search: "кофе", Offset: 5}
	original := f
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if f != original {
		t.Fatal("validation modified the filter")
	}
	normalized := f.Normalize()
	if normalized.Limit != 20 {
		t.Fatalf("default limit: %d", normalized.Limit)
	}
	normalized.Limit = original.Limit
	if f != original || normalized != original {
		t.Fatal("normalization changed fields other than the default limit")
	}
	for _, limit := range []int{-1, 1, 100, 101} {
		f := (Filter{Limit: limit}).Normalize()
		if f.Limit != limit {
			t.Fatalf("explicit limit %d was overwritten", limit)
		}
		if err := f.Validate(); (err != nil) != (limit < 0 || limit > 100) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func TestCategoriesCannotBeChangedByCaller(t *testing.T) {
	values := Categories()
	if len(values) != 7 {
		t.Fatalf("category count: %d", len(values))
	}
	original := values[0]
	values[0] = "изменённая категория"
	if !Category(original) || Category(values[0]) || Categories()[0] != original {
		t.Fatal("caller changed the accepted categories")
	}
}
