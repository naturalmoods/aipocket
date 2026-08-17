// Package jpath implements the small subset of JSONPath that provider
// manifests need. It is deliberately tiny: a full JSONPath engine would be a
// third-party dependency in a tool that handles API credentials, and the extra
// expressiveness buys nothing for reading a number out of a billing response.
//
// Supported grammar:
//
//	$.a.b            object traversal
//	$.a[0]           array index
//	$.a[?k=v]        first array element whose field k equals v (string compare)
//	$.a[?k=v].b      the above, then keep traversing
//
// Anything else is a manifest error, reported at load time by Validate.
package jpath

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/naturalmoods/aipocket/internal/money"
)

type stepKind int

const (
	stepKey stepKind = iota
	stepIndex
	stepFilter
)

type step struct {
	kind  stepKind
	key   string // stepKey, stepFilter field name
	index int    // stepIndex
	value string // stepFilter expected value
}

// Path is a compiled expression. The zero value is invalid; use Compile.
type Path struct {
	steps []step
}

// Compile parses expr. It returns an error for anything outside the grammar
// above so that a malformed manifest fails at load time, not at request time.
func Compile(expr string) (Path, error) {
	var p Path
	if !strings.HasPrefix(expr, "$") {
		return p, fmt.Errorf("jpath %q: must start with $", expr)
	}
	rest := expr[1:]

	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end == -1 {
				end = len(rest)
			}
			key := rest[:end]
			if key == "" {
				return p, fmt.Errorf("jpath %q: empty key", expr)
			}
			p.steps = append(p.steps, step{kind: stepKey, key: key})
			rest = rest[end:]

		case strings.HasPrefix(rest, "["):
			close := strings.Index(rest, "]")
			if close == -1 {
				return p, fmt.Errorf("jpath %q: unclosed [", expr)
			}
			inner := rest[1:close]
			rest = rest[close+1:]

			if strings.HasPrefix(inner, "?") {
				field, want, ok := strings.Cut(inner[1:], "=")
				if !ok || field == "" {
					return p, fmt.Errorf("jpath %q: filter must be [?field=value]", expr)
				}
				p.steps = append(p.steps, step{kind: stepFilter, key: field, value: want})
				continue
			}
			n, err := strconv.Atoi(inner)
			if err != nil || n < 0 {
				return p, fmt.Errorf("jpath %q: bad index %q", expr, inner)
			}
			p.steps = append(p.steps, step{kind: stepIndex, index: n})

		default:
			return p, fmt.Errorf("jpath %q: unexpected %q", expr, rest)
		}
	}
	return p, nil
}

// MustCompile is Compile for package-level constants in tests.
func MustCompile(expr string) Path {
	p, err := Compile(expr)
	if err != nil {
		panic(err)
	}
	return p
}

// Eval walks doc. A missing key or out-of-range index is not an error: it
// returns (nil, false), because "this provider did not include the field" is a
// normal outcome that the caller reports as "unknown", not as a failure.
func (p Path) Eval(doc any) (any, bool) {
	cur := doc
	for _, s := range p.steps {
		switch s.kind {
		case stepKey:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = m[s.key]
			if !ok {
				return nil, false
			}

		case stepIndex:
			a, ok := cur.([]any)
			if !ok || s.index >= len(a) {
				return nil, false
			}
			cur = a[s.index]

		case stepFilter:
			a, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			found := false
			for _, item := range a {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if asString(m[s.key]) == s.value {
					cur, found = item, true
					break
				}
			}
			if !found {
				return nil, false
			}
		}
	}
	return cur, true
}

// Number evaluates the path and coerces the result to float64. Providers are
// inconsistent about whether balances are JSON numbers or strings (DeepSeek
// returns strings, OpenRouter returns numbers), so both are accepted.
//
// Values that cannot be money are rejected rather than propagated. strconv
// accepts "NaN", "Inf" and hexadecimal floats such as "0x1p1000" without
// error; a NaN reaching the report makes the total NaN and then makes the whole
// JSON document unencodable, so one broken provider would blank out every other
// provider's balance.
func (p Path) Number(doc any) (float64, bool) {
	v, ok := p.Eval(doc)
	if !ok {
		return 0, false
	}
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case string:
		s := strings.TrimSpace(n)
		// Reject hexadecimal float syntax outright; only decimal is money.
		if strings.ContainsAny(s, "xXpP") {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if !money.Plausible(f) {
		return 0, false
	}
	return f, true
}

// asString is only for comparing a filter's field against its expected value —
// `$.a[?currency=USD]` — where the document may hold the value as a string, a
// number or a bool. It is not a way to read a value out of a response: nothing
// in a balance manifest wants a string, and an exported accessor for one existed
// unused, which is the sort of surface a tool that handles API keys should not
// be carrying.
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
