package domain

import "testing"

// The lenient policy is the default: an explicitly international number is a
// login identity even when the numbering plan cannot describe it. Short
// ranges and unassigned blocks stay usable, while a number without a country
// calling code is still refused.
func TestNormalizePhoneLenientPolicyAcceptsInternationalShortAndUnassigned(t *testing.T) {
	for input, want := range map[string]string{
		"+7 123":          "7123",
		"+1488":           "1488",
		"+44 7400 123456": "447400123456",
		"+222 12345678":   "22212345678",
		"+1 555 000 0001": "15550000001",
	} {
		if got := NormalizePhone(input); got != want {
			t.Fatalf("NormalizePhone(%q) = %q, want %q", input, got, want)
		}
	}
	// A national number that omits its country calling code stays refused in
	// the lenient policy: the digits are ambiguous between a national number
	// and a calling code.
	for _, input := range []string{"09981679461", "00989981679461", "07400123456"} {
		if got := NormalizePhone(input); got != "" {
			t.Fatalf("NormalizePhone(%q) = %q, want empty", input, got)
		}
	}
}

// The strict policy requires an assigned range of a real country calling code.
func TestNormalizePhoneStrictPolicyRequiresAssignedRange(t *testing.T) {
	ConfigurePhoneFormatCheck(true)
	t.Cleanup(func() { ConfigurePhoneFormatCheck(false) })
	for input, want := range map[string]string{
		"+44 7400 123456":     "447400123456",
		"+86 (188) 0000-0000": "8618800000000",
		"+98 0998 167 9461":   "989981679461",
	} {
		if got := NormalizePhone(input); got != want {
			t.Fatalf("NormalizePhone(%q) = %q, want %q", input, got, want)
		}
	}
	// Short ranges, unassigned blocks and unknown calling codes are refused.
	for _, input := range []string{"+7 123", "+1488", "+222 12345678", "+1 555 000 0001", "+999 123456789", "09981679461"} {
		if got := NormalizePhone(input); got != "" {
			t.Fatalf("strict NormalizePhone(%q) = %q, want empty", input, got)
		}
	}
}

// The reserved service identity stays resolvable under both policies so auth
// can recognize it as a system account instead of treating it as free.
func TestReservedSystemPhoneStaysResolvableUnderBothPolicies(t *testing.T) {
	for _, strict := range []bool{false, true} {
		ConfigurePhoneFormatCheck(strict)
		if got := NormalizePhone(OfficialSystemPhone); got != OfficialSystemPhone {
			t.Fatalf("strict=%v NormalizePhone(%q) = %q, want %q", strict, OfficialSystemPhone, got, OfficialSystemPhone)
		}
		if got := NormalizePhone("+42777"); got != OfficialSystemPhone {
			t.Fatalf("strict=%v NormalizePhone(\"+42777\") = %q, want %q", strict, got, OfficialSystemPhone)
		}
		if _, ok := SystemUserByPhone(OfficialSystemPhone); !ok {
			t.Fatalf("strict=%v SystemUserByPhone(%q) = false, want true", strict, OfficialSystemPhone)
		}
	}
	ConfigurePhoneFormatCheck(false)
}
