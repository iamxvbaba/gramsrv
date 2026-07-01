// Client constructor drift — method constructors emitted by a specific client's
// hand-maintained TL (DrKLO Android, TLRPC.java) that are absent from the
// canonical (227) schema. They are old-layer API versions the client never
// updated; from the server's view they request the SAME api method, only with an
// older wire shape.
//
// The generic inbound upgrader (inbound.go) matches each by qualified name to
// the canonical method and rebuilds a canonical request: copy shared fields,
// write 0 for inserted flags integers, synthesize defaults for new required
// fields, and convert changed field types via the converter registry. So adding
// support for a newly-observed drifted constructor is one line here + a `gen`
// run — never a runtime-discovered hand patch.
//
// Only bodies that differ structurally belong here. Body-identical drift (only
// the constructor id differs) is a plain id swap in clientMethodAliases
// (client_aliases.go).

---functions---

messages.uploadMedia#519bc2b1 peer:InputPeer media:InputMedia = MessageMedia;
auth.signUp#80eee427 phone_number:string phone_code_hash:string first_name:string last_name:string = auth.Authorization;
messages.getMessages#4222fa74 id:Vector<int> = messages.Messages;
channels.getMessages#93d7b347 channel:InputChannel id:Vector<int> = messages.Messages;
bots.exportBotToken#0063b089 bot_id:long revoke:Bool = bots.ExportedBotToken;
account.registerDevice#637ea878 token_type:int token:string = Bool;
contacts.search#11f812d8 q:string limit:int = contacts.Found;
langpack.getLangPack#9ab5c58e lang_code:string = LangPackDifference;
langpack.getStrings#2e1ee318 lang_code:string keys:Vector<string> = Vector<LangPackString>;
langpack.getLanguages#800fd57d = Vector<LangPackLanguage>;
// DrKLO still emits old channels.editCreator#8f38cd1f; canonical 227 replaced
// that flow with messages.editChatCreator(peer:InputPeer,...). Keep the old
// constructor id here but target the canonical method name for generic upgrade.
messages.editChatCreator#8f38cd1f channel:InputChannel user_id:InputUser password:InputCheckPasswordSRP = Updates;
