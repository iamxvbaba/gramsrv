package domain

import (
	"strings"
	"testing"
)

func TestPhonePolicyCanonicalIdentityBoundaries(t *testing.T) {
	original := PhoneFormatCheckEnabled()
	t.Cleanup(func() { ConfigurePhoneFormatCheck(original) })
	for _, strict := range []bool{false, true} {
		ConfigurePhoneFormatCheck(strict)
		accepted := []string{
			"+44 7400 123456", "+98 0998 167 9461", "+39 02 1234 5678",
			"+888 12-34", "888123456789012", OfficialSystemPhone,
		}
		if !strict {
			accepted = append(accepted, "+7 123", "1488", "999123456789", strings.Repeat("9", 32))
		}
		for _, input := range accepted {
			got := NormalizePhone(input)
			if got == "" || NormalizePhone(got) != got || !ValidPhone(got) {
				t.Fatalf("strict=%v input=%q canonical=%q is not a stable identity", strict, input, got)
			}
		}
		rejected := []string{
			"", "123", "01234", "+09981679461", "00989981679461",
			"888123", "8881234567890123", strings.Repeat("9", 33),
			"++447400123456", "44+7400123456", "+44abc7400123456",
		}
		if strict {
			rejected = append(rejected, "7123", "1488", "999123456789", strings.Repeat("9", 32))
		}
		for _, input := range rejected {
			if got := NormalizePhone(input); got != "" {
				t.Fatalf("strict=%v NormalizePhone(%q)=%q, want rejection", strict, input, got)
			}
		}
	}
}
