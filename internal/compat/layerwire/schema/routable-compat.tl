// Hand-maintained request layouts that are intentionally handled by the RPC
// fallback instead of gotd's canonical ServerDispatcher.  They still belong in
// the structural preflight model: fallback handlers must never become a way to
// bypass the canonical vector/depth/bytes budgets.

---functions---

compat.legacyCreateTheme#8432c21f flags:# slug:string title:string document:flags.2?InputDocument settings:flags.3?InputThemeSettings = Object;
compat.legacyUpdateTheme#5cb367d5 flags:# format:string theme:InputTheme slug:flags.0?string title:flags.1?string document:flags.2?InputDocument settings:flags.3?InputThemeSettings = Object;
compat.legacyInstallTheme#7ae43737 flags:# dark:flags.0?true format:flags.1?string theme:flags.1?InputTheme = Object;
compat.legacyGetTheme#8d9d742b format:string theme:InputTheme document_id:long = Object;
