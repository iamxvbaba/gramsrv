package domain

// PremiumPromoCatalog is the immutable, domain-only media catalog returned by
// help.getPremiumPromo. VideoSections[i] describes Videos[i]; callers must
// preserve the one-to-one ordering because official clients use positional
// lookup.
type PremiumPromoCatalog struct {
	VideoSections []string
	Videos        []Document
}
