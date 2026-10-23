// Audited DrKLO Android private/retained RPC constructors.
//
// These functions are not an official Telegram Layer. They are derived from
// the exact Java/C++ serializers locked by manifest.json and are compiled into
// a separate gotdgen client-RPC overlay. Nested constructors still decode with
// the connection's exact generated Layer profile.

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
messages.editChatCreator#8f38cd1f channel:InputChannel user_id:InputUser password:InputCheckPasswordSRP = Updates;

messages.forwardMessages#41d41ade flags:# silent:flags.5?true background:flags.6?true with_my_score:flags.8?true drop_author:flags.11?true drop_media_captions:flags.12?true noforwards:flags.14?true from_peer:InputPeer id:Vector<int> random_id:Vector<long> to_peer:InputPeer top_msg_id:flags.9?int reply_to:flags.22?InputReplyTo schedule_date:flags.10?int schedule_repeat_period:flags.24?int send_as:flags.13?InputPeer quick_reply_shortcut:flags.17?InputQuickReplyShortcut video_timestamp:flags.20?int allow_paid_stars:flags.21?long suggested_post:flags.23?SuggestedPost = Updates;
channels.inviteToChannel#199f3a6c channel:InputChannel users:Vector<InputUser> = messages.InvitedUsers;
messages.importChatInvite#6c50051c hash:string = messages.ChatInviteJoinResult;
channels.joinChannel#24b524c5 channel:InputChannel = messages.ChatInviteJoinResult;
updates.getDifference#25939651 flags:# pts:int pts_total_limit:flags.0?int date:int qts:int = updates.Difference;
messages.createChat#0034a818 flags:# users:Vector<InputUser> title:string ttl_period:flags.0?int = messages.InvitedUsers;
