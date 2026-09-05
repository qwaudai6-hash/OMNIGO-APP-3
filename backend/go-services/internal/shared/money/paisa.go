package money

import (
	"fmt"
	"math"
	"strconv"
)

// Paisa represents the smallest currency unit for PKR (1 PKR = 100 paisa).
// All money amounts in the ledger and escrow systems are stored as int64 paisa.
// This eliminates floating-point precision errors inherent in float64.
type Paisa int64

// Conversion constants
const (
	PaisaPerRupee int64 = 100
)

// RupeesToPaisa converts a rupee amount (e.g., 150.50) to paisa (15050).
// The input is a float64 from the DB or API layer. Use this at the boundary
// only — internal code should work with int64 paisa directly.
func RupeesToPaisa(rupees float64) int64 {
	return int64(math.Round(rupees * float64(PaisaPerRupee)))
}

// PaisaToRupees converts paisa back to rupees as float64 for display/JSON.
// Only use this at API boundaries where JSON consumers expect decimal format.
func PaisaToRupees(paisa int64) float64 {
	return float64(paisa) / float64(PaisaPerRupees())
}

// PaisaPerRupees returns the conversion factor (100).
func PaisaPerRupees() int64 {
	return PaisaPerRupee
}

// FormatPaisa formats paisa as a decimal string "1234.56" for display.
func FormatPaisa(paisa int64) string {
	rupees := paisa / PaisaPerRupee
	remainingPaisa := paisa % PaisaPerRupee
	if remainingPaisa < 0 {
		remainingPaisa = -remainingPaisa
	}
	return fmt.Sprintf("%d.%02d", rupees, remainingPaisa)
}

// PaisaToPayFastString converts paisa to PayFast-compatible decimal string.
// PayFast expects amounts like "1500.00" (not "1500").
func PaisaToPayFastString(paisa int64) string {
	return FormatPaisa(paisa)
}

// ParsePayFastAmount parses a PayFast decimal string to paisa.
// Returns 0 and error on invalid input.
func ParsePayFastAmount(s string) (int64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid PayFast amount %q: %w", s, err)
	}
	return RupeesToPaisa(f), nil
}

// CalculateCommission computes admin commission from total amount and rate (in basis points).
// Example: CalculateCommission(10000, 200) = 200 (2.00% of 10000 paisa = 200 paisa)
// The rate is in basis points: 200 = 2.00%, 1500 = 15.00%.
func CalculateCommission(amountPaisa int64, rateBasisPoints int64) int64 {
	return int64(math.Round(float64(amountPaisa) * float64(rateBasisPoints) / 10000.0))
}

// CalculateCommissionFromRate computes admin commission using a float rate (e.g., 2.00 for 2%).
// This is for backward compatibility with code that passes rates as 2.00, 15.00, etc.
func CalculateCommissionFromRate(amountPaisa int64, rate float64) int64 {
	return int64(math.Round(float64(amountPaisa) * rate / 100.0))
}

// Abs returns the absolute value of a paisa amount.
func Abs(p int64) int64 {
	if p < 0 {
		return -p
	}
	return p
}

// Min returns the smaller of two paisa amounts.
func Min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Max returns the larger of two paisa amounts.
func Max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
