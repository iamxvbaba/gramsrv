// Default NFT slug prefix from a gift title: "Durov's Cap" -> "DurovsCap",
// so collectibles are named DurovsCap-1, DurovsCap-2, ... like in Telegram.
export const slugFromTitle = (title: string) => title.replace(/[^A-Za-z0-9]/g, "").slice(0, 40);
