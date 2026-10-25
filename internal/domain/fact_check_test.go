package domain

import "testing"

func TestValidateFactCheck(t *testing.T) {
	if err := ValidateFactCheck(FactCheck{Text: "verdict"}); err != nil {
		t.Fatalf("ValidateFactCheck(text) = %v, want nil", err)
	}
	if err := ValidateFactCheck(FactCheck{Text: "   "}); err == nil {
		t.Fatal("ValidateFactCheck(blank) = nil, want error")
	}
	if err := ValidateFactCheck(FactCheck{Text: "verdict", Country: "de"}); err == nil {
		t.Fatal("ValidateFactCheck(lowercase country) = nil, want error")
	}
	if err := ValidateFactCheck(FactCheck{Text: "verdict", Country: "DE"}); err != nil {
		t.Fatalf("ValidateFactCheck(DE) = %v, want nil", err)
	}
	long := make([]rune, MaxFactCheckTextLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateFactCheck(FactCheck{Text: string(long)}); err == nil {
		t.Fatal("ValidateFactCheck(too long) = nil, want error")
	}
}

func TestFactCheckHashIsStablePerMessage(t *testing.T) {
	first := FactCheckHash(42, 7, FactCheck{Text: "verdict"})
	if first != FactCheckHash(42, 7, FactCheck{Text: "verdict"}) {
		t.Fatal("FactCheckHash is not stable for the same message and text")
	}
	if first == FactCheckHash(42, 8, FactCheck{Text: "verdict"}) {
		t.Fatal("FactCheckHash collides across message ids")
	}
	if first == FactCheckHash(43, 7, FactCheck{Text: "verdict"}) {
		t.Fatal("FactCheckHash collides across channels")
	}
	if first == FactCheckHash(42, 7, FactCheck{Text: "other verdict"}) {
		t.Fatal("FactCheckHash collides across texts")
	}
	if first == 0 {
		t.Fatal("FactCheckHash must never be zero")
	}
}

func TestFactCheckHashCoversVisibleContent(t *testing.T) {
	base := FactCheck{Text: "source", Country: "DE", Entities: []MessageEntity{{Type: MessageEntityTextURL, Offset: 0, Length: 6, URL: "https://example.com/a"}}}
	before := FactCheckHash(42, 7, base)
	for _, change := range []func(*FactCheck){
		func(c *FactCheck) { c.Country = "US" },
		func(c *FactCheck) { c.NeedCheck = true },
		func(c *FactCheck) { c.Entities[0].URL = "https://example.com/b" },
		func(c *FactCheck) { c.Entities[0].Type = MessageEntityBold },
	} {
		copy := base
		copy.Entities = append([]MessageEntity(nil), base.Entities...)
		change(&copy)
		if FactCheckHash(42, 7, copy) == before {
			t.Fatal("visible content changed without changing hash")
		}
	}
	base.Hash = 123
	if FactCheckHash(42, 7, base) != before {
		t.Fatal("stored hash affected the content hash")
	}
	if FactCheckHash(42, 7, FactCheck{Text: "x"}) != FactCheckHash(42, 7, FactCheck{Text: "x", Entities: []MessageEntity{}}) {
		t.Fatal("nil and empty entity vectors have different hashes")
	}
}
