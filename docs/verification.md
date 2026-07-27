# Official platform verification

Official verification is the platform badge shown next to the name of a bot,
public channel or public supergroup whose identity a human reviewer has
confirmed. In this server it is exactly one boolean on one peer record:
`users.verified` or `channels.verified`. Nothing else about the peer changes.

Applications are filed through the built-in `@verifybot`, decided in the admin
panel, and the decision commits together with the flag write. The protocol edge
then makes the new flag observable: it drops the cached peer projections and
pushes the ordinary peer-refresh update to online clients.

## What this is not

The badge here is the **platform** flag: `user#b1b8cc83 verified:flags.17` and
`channel#d49f34c6 verified:flags.7`.

Telegram also has a second, unrelated mechanism — **third-party bot
verification** — where an ordinary bot that a platform operator has appointed as
a "verifier" attaches its own custom icon and description to arbitrary peers.
That is `botVerification#f93cd45c`, `botVerifierSettings#b0cd6617`,
`bots.setCustomVerification#8b89dfbd`, `user.bot_verification_icon:flags2.14?long`,
`channel.bot_verification_icon:flags2.13?long` and
`channelFull.bot_verification:flags2.17?BotVerification`.

The two are deliberately kept apart:

- an approval in this flow never writes `bot_verification*`, and never issues
  `botVerifierSettings` to anybody;
- `bots.setCustomVerification` is routed and argument-checked at the RPC edge and
  then refused with `403 BOT_VERIFIER_FORBIDDEN`
  (`internal/rpc/bots_longtail.go`), because no bot on this deployment is a
  verifier;
- a client that renders `bot_verification_icon` renders it from data this flow
  never produces, so a third-party icon can neither stand in for the platform
  badge nor be shadowed by it.

## TL constructors and flags (Layer 228)

Checked against the schema snapshot the server is built for,
`/tmp/td/_schema/layers/layer-228.tl`.

Platform verification:

| Constructor | Field |
| --- | --- |
| `user#b1b8cc83` | `verified:flags.17?true` |
| `channel#d49f34c6` | `verified:flags.7?true` |
| `chatInvite#5c9d3702` | `verified:flags.7?true`, `scam:flags.8?true`, `fake:flags.9?true` |

`chatInviteAlready#5a686d7c` carries a whole `Chat`, so a member's preview gets
the badge through `channel#d49f34c6` rather than through invite-level flags.

Third-party bot verification, for contrast — none of these are written by this
flow:

| Constructor / method | Field |
| --- | --- |
| `botVerification#f93cd45c` | `bot_id:long icon:long description:string` |
| `botVerifierSettings#b0cd6617` | `icon:long company:string custom_description:flags.0?string` |
| `bots.setCustomVerification#8b89dfbd` | `enabled:flags.1?true bot:flags.0?InputUser peer:InputPeer custom_description:flags.2?string` |
| `user#b1b8cc83` | `bot_verification_icon:flags2.14?long` |
| `channel#d49f34c6` | `bot_verification_icon:flags2.13?long` |
| `channelFull#a04e8d3a` | `bot_verification:flags2.17?BotVerification` |

`chatInvite#5c9d3702` also has `bot_verification:flags.13?BotVerification`; it is
never populated, for the same reason.

## End-to-end path

1. **`@verifybot`** (user id `1250000011`, seeded by migration `0152`) collects
   the application in a step-by-step dialog: subject, category, description,
   official website, optional social links, independent press links, optional
   note. Commands are `/new`, `/status`, `/cancel`, `/help`.
2. **Eligibility, first gate.** The subject must be a bot, public channel or
   public supergroup with a public `@username`, created or administered by the
   applicant, not a built-in system entity, not already verified, and not
   scam/fake/frozen/deleted. Per-applicant rate limits, an open-application cap
   and a post-rejection cooldown also apply.
3. **Submission** writes `verification_applications` (status `submitted`) and an
   immutable `verification_application_events` row.
4. **Admin panel** lists the queue and lets a reviewer claim, approve or reject.
   Panel BFF routes are under `/api/verification/...`; the Admin API routes are
   `GET /v1/verification/applications`, `.../{id}`, `.../counts`,
   `POST .../{id}/claim|approve|reject` and `POST /v1/verification/revoke`.
   Every decision also goes through the shared admin command journal, so it lands
   in `admin_commands` / `admin_audit_logs`.
5. **Eligibility, second gate.** At approval time the target is re-loaded and
   re-evaluated against a fresh snapshot, so a target that turned scam, lost its
   username, was frozen or got verified by another route between filing and
   review is refused. The review queue cannot launder a state the submission path
   forbids.
6. **Decision transaction.** The status transition, the audit event, the
   applicant-notification outbox row and the `verified` flag write on the peer
   commit in one transaction (`verification.PeerVerifier` is invoked with the
   store transaction taken from the context). "Approved" and "the peer carries
   the badge" can never disagree.
7. **Protocol edge.** After the commit the service calls
   `rpc.Router.NotifyPeerVerified(ctx, domain.Peer)`
   (`internal/rpc/verification_notify.go`), which:
   - drops the cached peer projections for the target
     (`invalidateRPCProjectionForUser` / `invalidateRPCProjectionForChannel`);
   - for a user or bot, reuses `NotifyUserModerationFlagsChanged` — the same
     audience-wide, non-PTS `updateUser` fan-out the scam/fake flags use. The
     audience is `ModerationFlagAudience` (accounts that already see the peer),
     filtered to the ones currently online; each recipient gets the peer
     re-projected for itself;
   - for a channel, reuses `NotifyChannelChanged` →
     `channelStateMutationUpdates` → `pushChannelStateToMembersWithLinkedMonoforum`,
     i.e. `updateChannel` plus the refreshed `channel#d49f34c6` object to the
     channel's members (and the linked monoforum when there is one);
   - reports a clear error instead of panicking when the peer cannot be resolved,
     and is a no-op on a nil receiver. A push failure never invalidates the
     committed decision; the caller logs it and moves on.
8. **Applicant notification.** `@verifybot` messages the applicant from a durable
   outbox drained by a retrying worker, never from inside the decision
   transaction. Kinds are `approved`, `rejected`, `revoked`.
9. **What the client sees.** An online client applies the flag from the pushed
   `User`/`Channel` object immediately: the badge appears in the dialog list,
   profile, search results and message headers without a restart. An offline
   client converges on reconnect — see below.

## Offline convergence

`updateUser` and `updateChannel` carry no `pts`, so they are not stored as
message-box events and are not replayed by `updates.getDifference`. Offline
sessions converge because `verified` is part of the peer's **base read model**,
whose version is bumped by the triggers shipped in `0001_init`:

- `users.verified` is listed in `telesrv_notify_user_base_read_model` (trigger
  `users_read_model_changed`), which bumps `user_base`, `contact_account` and the
  private dialog-light models, and in
  `telesrv_notify_user_channel_participants_read_model` (trigger
  `users_channel_participants_read_model_changed`), which bumps
  `channel_participants`;
- `channels` bumps `channel_base` on every row change (trigger
  `channels_read_model_changed`) and additionally fires
  `pg_notify('telesrv_channel_changed')`.

The `user_base` notification is consumed by the read-model listener
(`internal/store/postgres/read_model_listener.go`), which invalidates the RPC
projections **and** the shared Redis `user:base` row across instances. So any
later authoritative read — `users.getUsers`, `users.getFullUser`,
`channels.getChannels`, `channels.getFullChannel`, `messages.getDialogs`, or the
`users`/`chats` vectors attached to a `getDifference` answer — already carries the
new flag. No migration is needed for this, and none was added.

## Where the flag surfaces

`verified` is projected wherever a `User` or `Channel` object is projected, which
is every one of these:

| Surface | Method | Constructor |
| --- | --- | --- |
| Dialog list | `messages.getDialogs`, `messages.getPeerDialogs` | `user`, `channel` in `users`/`chats` |
| Search | `contacts.search`, `contacts.resolveUsername`, `messages.searchGlobal`, `channels.getAdminedPublicChannels` | `user`, `channel` |
| Profile | `users.getUsers`, `users.getFullUser` | `user` in `users.userFull.users` |
| Channel info | `channels.getChannels`, `channels.getFullChannel` | `channel` in `chats` |
| Message history | `messages.getHistory`, `messages.getMessages`, channel history | `user`, `channel` in `users`/`chats` |
| Invite preview | `messages.checkChatInvite` | `chatInvite` (`verified:flags.7`) or `chatInviteAlready.chat` |
| Live updates | pushed `updates` envelopes | `updateUser` / `updateChannel` plus the peer object |
| Difference | `updates.getDifference`, `updates.getChannelDifference` | `user`, `channel` in `users`/`chats` |

The invite preview is the one that used to be missing: before, a non-member saw
an unbadged preview and the badge only appeared after joining. It is now set from
the persistent channel record in `internal/rpc/channels_invites.go`, through the
generated `Set*` helpers so the `flags` word and the struct field stay in step,
and left entirely unset for an unflagged peer.

## Migrations

- **`0153_verify_service_bot`** seeds `@verifybot` (id `1250000011`, fixed
  `access_hash` double-written with `domain.VerifyBotAccessHash`), its `bots` row
  and command list, and its `peer_usernames` registry entry. The handle is
  occupied from the moment the schema is current, so an ordinary user cannot claim
  `@verifybot` in the window before first use.
- **`0154_verification_applications`** creates
  `verification_applications` (the durable audit subject, never deleted, moved
  through `draft → submitted → in_review → approved|rejected|cancelled` under an
  optimistic-locking `version`), the append-only
  `verification_application_events` history, and
  `verification_notification_outbox` with
  `UNIQUE (application_id, kind)` so a repeated approve delivers one message.
  Partial unique indexes enforce one live application per target and one draft per
  applicant.

Neither migration touches the `verified` columns or the read-model triggers:
`users.verified` and `channels.verified` already existed and were already covered.

## Configuration

Verification (`internal/config/config.go`, `.env.example`):

| Key | Default | Meaning |
| --- | --- | --- |
| `TELESRV_VERIFICATION_ENABLED` | `true` | Master switch. When off every use case refuses explicitly; peers already badged keep the badge. |
| `TELESRV_VERIFICATION_ALLOW_USER_TARGETS` | `false` | Whether plain user accounts may be subjects. |
| `TELESRV_VERIFICATION_REJECT_COOLDOWN` | `720h` | Wait before re-filing the same target after a rejection, measured from the decision. `0` disables; max `8760h`. |
| `TELESRV_VERIFICATION_APPLY_RATE_LIMIT` | `3` | Applications one applicant may create per window. `0` disables. |
| `TELESRV_VERIFICATION_APPLY_RATE_WINDOW` | `24h` | That window. |
| `TELESRV_VERIFICATION_BOT_RATE_LIMIT` | `30` | `@verifybot` dialog rate per applicant, independent of applications created. `0` disables. |
| `TELESRV_VERIFICATION_BOT_RATE_WINDOW` | `1m` | That window. |
| `TELESRV_VERIFICATION_NOTIFY_INTERVAL` | `15s` | Applicant-notification worker cadence. Must be positive. |
| `TELESRV_VERIFICATION_NOTIFY_BATCH` | `50` | Rows per cycle, `1..500`. |
| `TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER` | `3` | Open applications per applicant. `0` disables, max `50`. |

Reviewer access:

| Key | Default | Meaning |
| --- | --- | --- |
| `TELESRV_ADMIN_UI_PERMISSIONS` | `*` | Permissions of an Admin UI session. Reviewing needs `verification.review`; clearing an existing badge needs `verification.revoke` on top of it. |
| `TELESRV_ADMIN_SCOPED_TOKENS` | *(empty)* | `name:token:perm1,perm2` entries separated by `;`, for Admin API integrations that should get `verification.review` and nothing else. |

## Manual check with official Telegram Desktop

1. Log in to the deployment with official Telegram Desktop.
2. Open `@verifybot` — it resolves by username and its own profile already shows
   the badge (the seed row is `verified`).
3. Send `/new`, pick the subject from the inline picker, and answer the steps:
   category, description, official website, social links (or *Skip*), at least the
   required number of independent press links, optional note. Press
   *Submit application*.
4. Send `/status`; the application is listed as submitted.
5. In the admin panel open the verification queue, claim the application and
   approve it. Confirm the audit entry appeared.
6. Within one notification-worker interval `@verifybot` messages the applicant
   with the decision.
7. Without restarting the client, check the badge on the approved subject:
   - **Profile** — open the peer's profile; the badge sits next to the title.
   - **Search** — type the `@username` in global search; the result row is badged.
   - **Dialog list** — the chat row in the main list is badged.
   - **Message header** — open the chat; the header title is badged.
   - **Invite preview** — from a *second* account that is **not** a member, open
     an invite link to the approved channel. The join-confirmation box is badged
     before joining. (This is the `chatInvite#5c9d3702 verified:flags.7` path.)
8. To check offline convergence, quit the client before approving, approve, then
   start it again: the badge is present on the first read, delivered by
   `getDifference` and the peer reads it triggers rather than by a live push.
9. Revoking from the panel takes the badge away by the same route.

## Limitations

- **Plain user accounts are off by default.** With
  `TELESRV_VERIFICATION_ALLOW_USER_TARGETS=false` (the shipped default) an
  application whose subject is an ordinary account is refused
  (`ErrVerificationUserTargetsDisabled`). Turning it on does not add any extra
  identity checks; it only stops refusing the target type.
- **Third-party bot verification is not part of this mechanism and is not
  implemented.** `bots.setCustomVerification` never succeeds: it validates its
  arguments and then refuses with `403 BOT_VERIFIER_FORBIDDEN`. No
  `botVerifierSettings` are issued, and
  `user.bot_verification_icon` / `channel.bot_verification_icon` /
  `channelFull.bot_verification` / `chatInvite.bot_verification` are never
  populated. A client asking for a custom verifier icon gets nothing.
- **The badge has no attributes.** It is a single boolean: no verifier company, no
  custom description, no per-peer icon, no expiry, no verification tier. There is
  nothing in the TL surface to carry them for the platform flag.
- **Built-in system entities cannot be applied for.** Service accounts are refused
  with `ErrVerificationTargetSystem`; their badge is set by the seed migrations.
- **A subject with no public `@username` is refused**
  (`ErrVerificationTargetNotPublic`), so private channels and usernameless bots
  cannot be verified at all — not even by an operator using the panel's revoke
  route in reverse.
- **Legacy basic groups (`chat#…`) have no `verified` field** in Layer 228, so a
  non-migrated basic group can never show a badge regardless of what is stored.
- **The live push only reaches online sessions.** `NotifyPeerVerified` filters the
  audience by the online index; everybody else converges on their next
  authoritative read. The user fan-out is additionally bounded (the moderation
  audience is capped, currently at 4096 accounts) and the channel fan-out is
  bounded by the online-member index, so on a very large peer some sessions get
  the flag from their next read rather than from a push.
- **`updateUser` / `updateChannel` carry no `pts`.** They are not persisted as
  message-box events, so a session that was offline during the decision never
  replays the push itself; it depends on the read-model bump. That is by design
  (a badge change is not a message), but it means the badge is not guaranteed to
  arrive as an *event* — only as *state*.
- **The live push can be one beat behind the shared base-user cache.** The
  decision writes the user row inside the verification transaction, bypassing the
  `users` service and therefore its Redis `user:base` refresh; that cache is
  dropped cross-instance by the asynchronous `user_base` read-model
  notification. `NotifyPeerVerified` runs synchronously right after commit, so in
  the small window before the listener processes the event the pushed `user`
  object can still carry the pre-decision flag. The persisted state is always
  correct, the projections are always invalidated, and the client repairs itself
  on the next read, so this shows up at worst as a badge that appears a moment
  late rather than instantly. The channel path is not affected: the
  transaction-scoped channel store is handed the channel row cache and drops it
  on the flag write.
- **Nothing re-checks a verified peer over time.** Once badged, a peer keeps the
  badge until an operator revokes it. There is no periodic re-validation, and
  losing the username or picking up a scam flag later does not clear it.
