// Default NFT slug prefix from a gift title: "Durov's Cap" -> "DurovsCap",
// so collectibles are named DurovsCap-1, DurovsCap-2, ... like in Telegram.
// A title without Latin letters (Cyrillic, emoji, "#123") gives "" so the
// caller falls back to giftSlugFallback instead of a string of digits.
export const slugFromTitle = (title: string) => {
  const slug = title.replace(/[^A-Za-z0-9]/g, "").slice(0, 40);
  return /[A-Za-z]/.test(slug) ? slug : "";
};

// giftSlugFallback is "Gift" plus the tail of the id: "Gift081615".
export const giftSlugFallback = (id: string | number) => `Gift${String(id).slice(-6)}`;

// defaultGiftSlug picks the title-based slug, else the readable fallback.
export const defaultGiftSlug = (title: string, id: string | number) => slugFromTitle(title) || giftSlugFallback(id);
