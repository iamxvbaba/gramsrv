// Audited DrKLO Android theme RPC constructors from TL_account.java.
// Kept separate from the 15 long-lived core private methods so provenance,
// coverage counts, and removal decisions remain explicit.

---functions---

account.createTheme#8432c21f flags:# slug:string title:string document:flags.2?InputDocument settings:flags.3?InputThemeSettings = Theme;
account.updateTheme#5cb367d5 flags:# format:string theme:InputTheme slug:flags.0?string title:flags.1?string document:flags.2?InputDocument settings:flags.3?InputThemeSettings = Theme;
account.installTheme#7ae43737 flags:# dark:flags.0?true format:flags.1?string theme:flags.1?InputTheme = Bool;
account.getTheme#8d9d742b format:string theme:InputTheme document_id:long = Theme;
