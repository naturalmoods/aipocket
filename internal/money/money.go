// Package money holds the one numeric rule the whole tool shares: what may be
// accepted as a money figure.
//
// It exists as a package rather than a constant in each caller because the
// figures arrive from three unrelated directions — a JSON field parsed by
// internal/jpath, a difference computed by internal/manifest, and a number the
// user typed into the config read by internal/core — and each one used to
// enforce its own rules, or none. A value that passes one check and fails
// another is how a NaN reaches the report and makes the whole JSON document
// unencodable, blanking out every other provider's balance.
package money

import "math"

// MaxPlausible bounds what may be accepted as a money figure. No prepaid LLM
// balance is a trillion units of anything; a value above this is a parsing
// artefact or a hostile response, and accepting it would poison every total it
// is summed into.
const MaxPlausible = 1e12

// Plausible reports whether f may be treated as an amount of money.
//
// Non-finite values are rejected because strconv.ParseFloat accepts "NaN",
// "Inf" and hexadecimal floats such as "0x1p1000" without error, and because a
// single NaN propagates into every total it touches.
func Plausible(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) <= MaxPlausible
}
