package jpath

import (
	"encoding/json"
	"testing"
)

func doc(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test json: %v", err)
	}
	return v
}

func TestCompileRejectsMalformed(t *testing.T) {
	for _, expr := range []string{
		"data.total",    // no $
		"$.",            // empty key
		"$.a[",          // unclosed
		"$.a[x]",        // non-numeric index
		"$.a[-1]",       // negative index
		"$.a[?nofield]", // filter without =
		"$.a[?=v]",      // filter without field
		"$a",            // missing separator
	} {
		if _, err := Compile(expr); err == nil {
			t.Errorf("Compile(%q) should have failed", expr)
		}
	}
}

func TestNumber(t *testing.T) {
	d := doc(t, `{"data":{"total_credits":50,"total_usage":12.3456}}`)
	got, ok := MustCompile("$.data.total_credits").Number(d)
	if !ok || got != 50 {
		t.Fatalf("got %v %v, want 50 true", got, ok)
	}
}

// Providers disagree on whether money is a JSON number or a string; DeepSeek
// returns strings. Both must work or the manifest author has to know which.
func TestNumberAcceptsStringAmounts(t *testing.T) {
	d := doc(t, `{"balance":" 18.42 "}`)
	got, ok := MustCompile("$.balance").Number(d)
	if !ok || got != 18.42 {
		t.Fatalf("got %v %v, want 18.42 true", got, ok)
	}
}

func TestFilterSelectsByField(t *testing.T) {
	d := doc(t, `{"balance_infos":[
		{"currency":"CNY","total_balance":"7.00"},
		{"currency":"USD","total_balance":"18.42"}]}`)

	usd, ok := MustCompile("$.balance_infos[?currency=USD].total_balance").Number(d)
	if !ok || usd != 18.42 {
		t.Fatalf("USD: got %v %v, want 18.42 true", usd, ok)
	}
	cny, ok := MustCompile("$.balance_infos[?currency=CNY].total_balance").Number(d)
	if !ok || cny != 7 {
		t.Fatalf("CNY: got %v %v, want 7 true", cny, ok)
	}
}

// A missing field is a normal outcome, not an error: the caller reports it as
// "unknown". Returning zero-with-ok would silently invent a balance of 0.00.
func TestMissingFieldIsNotZero(t *testing.T) {
	d := doc(t, `{"data":{}}`)
	if v, ok := MustCompile("$.data.total_credits").Number(d); ok {
		t.Fatalf("missing field reported as present with value %v", v)
	}
	if _, ok := MustCompile("$.nope[?currency=USD].x").Number(d); ok {
		t.Fatal("filter on missing array reported as present")
	}
	if _, ok := MustCompile("$.data[0]").Number(d); ok {
		t.Fatal("index into non-array reported as present")
	}
}

func TestIndexAndNesting(t *testing.T) {
	d := doc(t, `{"a":[{"b":{"c":3}}]}`)
	if v, ok := MustCompile("$.a[0].b.c").Number(d); !ok || v != 3 {
		t.Fatalf("got %v %v, want 3 true", v, ok)
	}
	if _, ok := MustCompile("$.a[5].b").Number(d); ok {
		t.Fatal("out-of-range index reported as present")
	}
}

func TestBoolEval(t *testing.T) {
	d := doc(t, `{"is_available":false}`)
	v, ok := MustCompile("$.is_available").Eval(d)
	if !ok {
		t.Fatal("is_available not found")
	}
	if b, isBool := v.(bool); !isBool || b {
		t.Fatalf("got %#v, want false", v)
	}
}

// strconv.ParseFloat accepts these without error. A NaN balance makes the
// total NaN and then makes the whole JSON report unencodable, so one broken
// provider would erase every other provider's figure.
func TestNonMoneyValuesAreRejected(t *testing.T) {
	for _, raw := range []string{
		`{"b":"NaN"}`, `{"b":"nan"}`, `{"b":"Inf"}`, `{"b":"+Inf"}`,
		`{"b":"infinity"}`, `{"b":"0x1p1000"}`, `{"b":"0x10"}`,
		`{"b":1e300}`, `{"b":-1e300}`, `{"b":true}`, `{"b":null}`, `{"b":{}}`,
	} {
		d := doc(t, raw)
		if v, ok := MustCompile("$.b").Number(d); ok {
			t.Errorf("%s was accepted as the number %v", raw, v)
		}
	}
}

func TestPlausibleAmountsStillPass(t *testing.T) {
	for raw, want := range map[string]float64{
		`{"b":0}`:          0,
		`{"b":-5.25}`:      -5.25, // a direct field may legitimately be negative
		`{"b":"1e3"}`:      1000,
		`{"b":999999999}`:  999999999,
		`{"b":"0.000001"}`: 0.000001,
	} {
		d := doc(t, raw)
		got, ok := MustCompile("$.b").Number(d)
		if !ok || got != want {
			t.Errorf("%s -> %v %v, want %v true", raw, got, ok, want)
		}
	}
}
