package money

import (
	"math"
	"testing"
)

// Each of these used to be accepted somewhere: NaN and Inf through
// strconv.ParseFloat, an oversized figure through a total-minus-used
// subtraction of two individually plausible operands, and any of the three
// through a `manual:` value typed into the config.
func TestNothingThatIsNotMoneyIsAccepted(t *testing.T) {
	for _, f := range []float64{
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
		MaxPlausible * 1.000001,
		-MaxPlausible * 1.000001,
		math.MaxFloat64,
	} {
		if Plausible(f) {
			t.Errorf("Plausible(%v) = true", f)
		}
	}
}

func TestOrdinaryAmountsAreAccepted(t *testing.T) {
	for _, f := range []float64{0, 0.01, 37.6544, -12.5, MaxPlausible} {
		if !Plausible(f) {
			t.Errorf("Plausible(%v) = false", f)
		}
	}
}
