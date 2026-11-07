package domain

import (
	"testing"

	"organon/internal/kernel"
)

// Gap #42: formatNumber rounds decimals and decimal text exactly and
// formats them by the locale; other text is shown unchanged.
func TestGap42FormatNumberDecimals(t *testing.T) {
	e := &docEval{t: &DocumentTemplate{locale: "el"}}
	for _, tc := range []struct {
		n      any
		digits int
		want   string
	}{
		{kernel.MustDecimal("1234.005"), 2, "1.234,01"},
		{"1234.005", 2, "1.234,01"},
		{"145", 2, "145,00"},
		{"-0.004", 2, "0,00"},
		{"12345678901234567.125", 2, "12.345.678.901.234.567,13"},
		{"N/A", 2, "N/A"},
		{2.5, 0, "3"},
	} {
		if got := e.formatNumber(tc.n, tc.digits); got != tc.want {
			t.Errorf("formatNumber(%v, %d) = %q, want %q", tc.n, tc.digits, got, tc.want)
		}
	}
}
