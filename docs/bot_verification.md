# Third-party bot verification

Third-party verification is an **attributed** mark: a bot that the operator
appointed as a *verifier* attaches its own custom-emoji icon and one line of
description to a peer. Official clients draw that icon **before** the peer's name
and show the description in the profile, together with the name of the company the
verifier vouches under.

Reference material:

- <https://core.telegram.org/api/bots/verification>
- <https://telegram.org/verify#third-party-verification>

Applications are collected by the built-in `@verifierbot`, decided in the admin
panel, and the decision commits together with the mark write. The protocol edge then
drops the cached peer projections and pushes the ordinary peer-refresh update, so an
online client shows the icon without a restart.

## What this is not

This is **not** the platform checkmark. That one is a single boolean on the peer
(`users.verified` / `channels.verified`), granted by the operator after platform
review, collected by `@verifybot`, and documented in [`verification.md`](verification.md).

The two mechanisms are deliberately disjoint:

| | Official verification | Third-party verification |
| --- | --- | --- |
| Stored as | `users.verified`, `channels.verified` (boolean) | `bot_verifier_settings`, `custom_verifications` (attributed rows) |
| Granted by | the platform operator | a verifier bot the operator appointed |
| Rendered as | the standard checkmark **after** the name | the verifier's custom emoji **before** the name, plus a profile description |
| Front door | `@verifybot` | `@verifierbot` (or `bots.setCustomVerification` directly) |
| Panel section | *Official verification* (`/verification`) | *Third-party verification* (`/bot-verification`) |
| Permissions | `verification.review`, `verification.revoke` | `botverification.review`, `botverification.manage` |

Neither reads the other's tables. Both can sit on one peer at the same time, an
approval on one side never writes the other side's state, and revoking one leaves
the other alone. The admin panel repeats that distinction in the section header and
on every decision page, because "verified" in a ticket almost always means the other
one.

## TL constructors and flags (Layer 228)

Checked against the schema snapshot the server is built for,
`/tmp/td/_schema/layers/layer-228.tl`.

| Constructor / method | Field |
| --- | --- |
| `botVerification#f93cd45c` | `bot_id:long icon:long description:string` |
| `botVerifierSettings#b0cd6617` | `can_modify_custom_description:flags.1?true icon:long company:string custom_description:flags.0?string` |
| `bots.setCustomVerification#8b89dfbd` | `enabled:flags.1?true bot:flags.0?InputUser peer:InputPeer custom_description:flags.2?string = Bool` |
| `user#b1b8cc83` | `bot_verification_icon:flags2.14?long` |
| `channel#d49f34c6` | `bot_verification_icon:flags2.13?long` |
| `userFull#6cbe645` | `bot_verification:flags2.12?BotVerification` |
| `channelFull#a04e8d3a` | `bot_verification:flags2.17?BotVerification` |
| `chatInvite#5c9d3702` | `bot_verification:flags.13?BotVerification` |
| `botInfo#4d8a0299` | `verifier_settings:flags.9?BotVerifierSettings` |

One fact — "verifier *B* marked peer *P* with icon *I* and description *D*" — is
spread over six unrelated constructors, and a client renders the badge only when the
exact bit is set. Every projection therefore goes through the generated `Set*`
helpers (`internal/rpc/bot_verification_projection.go`): a struct field assigned
without its flag bit encodes as an absent field, and the badge silently disappears.

Note the asymmetry inside `botVerifierSettings`: `custom_description:flags.0` is the
operator-configured *default* description, while
`can_modify_custom_description:flags.1` is the permission that lets the verifier
override it per peer. `botVerification.description` is the resolved text actually
shown on a marked peer.

## The icon is a custom emoji document

`botVerification.icon` and `botVerifierSettings.icon` are custom emoji **document
ids**. A client resolves them through `messages.getCustomEmojiDocuments` — exactly
the reader `files.Service.GetDocuments` answers from on this server.

Consequences that shape the whole feature:

- An id that names no fetchable document renders as **nothing at all**: the peer is
  marked in the database and the client draws an empty space. Nothing errors, nothing
  logs on the client, and the operator sees a granted mark that users cannot see.
- Therefore the icon is never a free-form number. `verification_icons` is a
  catalogue, `botverification.Service.UpsertIcon` resolves the document before
  writing the row and refuses anything that is not a custom emoji
  (`domain.Document.IsCustomEmoji`), and a grant may only reference a catalogue entry
  that is `active` and either shared or reserved for that bot
  (`VerificationIcon.UsableBy`).
- The mark denormalises the icon at grant time (`custom_verifications.icon_document_id`),
  so a verifier changing its own icon later does not silently re-brand the peers it
  already marked.

The panel exposes the same rule: the icon catalogue tab is where document ids are
registered and named, and the grant form only offers active catalogue entries.

## End-to-end path

1. **Icon catalogue.** An operator adds a custom emoji document to
   `verification_icons` (panel: *Third-party verification → Icon catalogue*, or
   `POST /api/actions/upsert-verification-icon`). Entries can be shared or reserved
   for one bot, and retiring an entry (`set-verification-icon-active`) stops new
   grants without touching marks that already carry it.
2. **Verifier status.** The operator grants a bot verifier status — an icon from the
   catalogue, a company name, an optional default description and
   `can_modify_custom_description` (panel: *Verifiers*, or
   `POST /api/actions/grant-bot-verifier`). The row in `bot_verifier_settings` *is*
   verifier status: it is the only authority `bots.setCustomVerification` consults,
   and it is projected as `botInfo.verifier_settings`. Nothing seeds it — not even
   migration `0155` for the built-in bot — because seeding verifier status would ship
   a badge printer with the schema.
3. **Two ways to reach a mark.**
   - **Direct RPC.** The verifier bot (or the user who owns it) calls
     `bots.setCustomVerification`. `internal/rpc/bots_longtail.go` resolves the two
     TL branches — `bot:flags.0` unset means "the caller is the bot", set means "a
     user acting through a bot it owns" — validates shape, and hands a
     `domain.SetCustomVerificationRequest` to the service. A missing or disabled
     verifier row answers `403 BOT_VERIFIER_FORBIDDEN`, and the error deliberately
     does not distinguish "never was a verifier" from "switched off".
   - **Application queue.** A peer owner talks to `@verifierbot`
     (`/verify`, `/status`, `/revoke`, `/cancel`, `/help`), picks one of their own
     bots, channels or their own account, states a reason and optionally a wanted
     description. The bot writes `custom_verification_requests` with status
     `pending`; a partial unique index keeps one pending row per
     (verifier, peer) pair. `@verifierbot` decides nothing — it says so in `/start`.
4. **Review.** The panel lists the queue, the verifier roster, the icon catalogue and
   every granted mark. BFF routes are `GET /api/botverification/{verifiers,icons,marks,requests,counts}`,
   `GET /api/botverification/requests/{id}` and
   `POST /api/botverification/requests/{id}/{approve,reject,revoke}`; the manage-only
   mutations are the `/api/actions/...` commands listed above plus
   `set-bot-verifier-enabled`, `revoke-bot-verifier` and
   `revoke-custom-verification`. Every mutation goes through the shared admin command
   journal (reason → dry run → confirm), so it lands in `admin_commands` /
   `admin_audit_logs`.
5. **Second gate at approval.** `botverification.Service.Approve` re-loads a fresh
   snapshot: the verifier must still exist and be enabled, the peer must still
   resolve, and the per-verifier quota is spent only when the approval would create a
   mark rather than update one. A queue that sat for days cannot launder a state the
   RPC path would refuse. `version` is an optimistic lock — a stale panel gets a
   `409` and the page reloads instead of overwriting a fresher decision.
6. **Decision transaction.** The status transition and the mark write commit
   together: `DecideCustomVerificationRequest` runs the grant (or the revoke)
   through a callback whose context carries the decision's own transaction. "Approved"
   and "the peer carries the mark" can never disagree. The description is resolved by
   `BotVerifierSettings.DescriptionFor` — the applicant's wording only when
   `can_modify_custom_description` is set, otherwise the operator default — which is
   the single place that rule lives, so the RPC edge, the bot dialog and the panel
   preview cannot drift.
7. **Protocol edge.** After the commit the service calls
   `rpc.Router.NotifyPeerBotVerification(ctx, domain.Peer)`
   (`internal/rpc/bot_verification_notify.go`), which:
   - drops the cached peer projections for the peer, and for a channel also the
     `channelFull` bot-info cache that carries `botInfo.verifier_settings`;
   - for a user or bot, reuses `NotifyUserModerationFlagsChanged` — the audience-wide,
     non-PTS `updateUser` fan-out the scam/fake flags use, filtered to online
     sessions, with the peer re-projected per recipient;
   - for a channel, reuses `NotifyChannelChanged` → `updateChannel` plus the refreshed
     `channel#d49f34c6` object to members (and a linked monoforum when there is one);
   - is a no-op on a nil receiver and reports an error rather than panicking. A push
     failure never invalidates the committed decision.
8. **Applicant notification.** `@verifierbot` messages the applicant with the
   outcome (`SendVerificationDecision`). `internal_note` is never rendered there
   under any status — only `decision_reason` reaches the applicant.
9. **What the client sees.** The icon appears before the name in the dialog list,
   search results, message headers and the profile, and the description appears in the
   profile. `bots.setCustomVerification` returns `BoolTrue` for every successful
   application, including an idempotent re-apply or revoke; official clients
   treat `BoolFalse` as failure.

## Where the mark surfaces

| Surface | Method | Field |
| --- | --- | --- |
| Dialog list, search, history, difference | `messages.getDialogs`, `contacts.search`, `contacts.resolveUsername`, `messages.getHistory`, `updates.getDifference`, … | `user.bot_verification_icon`, `channel.bot_verification_icon` |
| User profile | `users.getFullUser` | `userFull.bot_verification` |
| Channel / supergroup info | `channels.getFullChannel` | `channelFull.bot_verification` |
| Invite preview (non-member) | `messages.checkChatInvite` | `chatInvite.bot_verification` |
| Verifier bot's own profile | `users.getFullUser`, `channels.getFullChannel` bot list | `botInfo.verifier_settings` |
| Live updates | pushed `updates` envelopes | `updateUser` / `updateChannel` plus the peer object |

The icon overlay runs at the **response boundary**, not inside `tgUser`/`tgChannel`:
`applyPeerReadModels` (`internal/rpc/story_peer_projection.go`) is the single hook
every handler funnels through, so all ~40 call sites get the field with one batched
read per response instead of an N+1 per peer. The `userFull` / `channelFull` /
`chatInvite` variants are post-cache overlays for the same reason — a cached full
object is still stamped with the current mark. A nil service or any read error leaves
every flag unset, which is byte-identical to the pre-feature wire shape.

## Migrations

- **`0155_bot_verification`** creates the four tables:
  - `verification_icons` — the catalogue. `document_id` is unique and positive,
    `owner_bot_id = 0` means shared, `active` retires an entry without deleting it.
  - `bot_verifier_settings` — verifier status, keyed by `bot_id` with an optimistic
    `version`, `enabled` as the per-verifier kill switch, and the operator's
    `granted_by` / `grant_reason` for the audit trail.
  - `custom_verifications` — granted marks. `UNIQUE (peer_type, peer_id)`
    matches the single `BotVerification` value on the wire: a different verifier
    replaces the current mark rather than leaving hidden fallback rows. It also has
    `peer_type IN ('user','channel')`,
    `icon_document_id` denormalised from the verifier, `ON DELETE CASCADE` from the
    verifier row.
  - `custom_verification_requests` — the review queue. `status IN ('pending','approved','rejected','revoked')`,
    a partial unique index for one `pending` row per (verifier, peer), and check
    constraints that pair each stamp with its status
    (`(status = 'approved') = (approved_at IS NOT NULL)`) and refuse a rejection
    without a reason.
- **`0156_verifier_service_bot`** seeds `@verifierbot` (id `1250000013`, fixed
  `access_hash` double-written with `domain.VerifierBotAccessHash`), its `bots` row
  and command list, and its `peer_usernames` registry entry, so the handle is occupied
  from the moment the schema is current. `verified = false` on purpose: a third-party
  verifier wearing the platform checkmark would blur the exact distinction it has to
  explain to every applicant. The seed grants **no** verifier status — an operator
  does that by hand in the panel.

Neither migration adds read-model triggers: the marks are read live at the response
boundary rather than cached in a peer read model.

## Configuration

Third-party verification (`internal/config/config.go`, `.env.example`):

| Key | Default | Meaning |
| --- | --- | --- |
| `TELESRV_BOT_VERIFICATION_ENABLED` | `true` | Master switch. When off, every third-party mutation is refused (grants, revocations, applications, catalogue edits) while marks already granted keep rendering — blanking one verifier's badges is what its per-verifier kill switch is for. |
| `TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER` | `10000` | Peers one verifier may mark. `0` disables the service bound and leaves only the storage bound (`domain.MaxCustomVerificationsPerVerifier`), which is also the maximum this key accepts. |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT` | `5` | Applications one applicant may file per window, across all verifier bots. `0` disables the budget. Looser than the official `3` on purpose: a deployment can run several verifier companies, and filing with a second one is not a retry of the first. |
| `TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW` | `24h` | That window. A positive limit requires a positive window. |

Operator access:

| Key | Default | Meaning |
| --- | --- | --- |
| `TELESRV_ADMIN_UI_PERMISSIONS` | `*` | Permissions of an Admin UI session. Reading the section and deciding applications needs `botverification.review`; the verifier roster, the icon catalogue and stripping a granted mark need `botverification.manage`. |
| `TELESRV_ADMIN_SCOPED_TOKENS` | *(empty)* | `name:token:perm1,perm2` entries separated by `;`, for integrations that should get `botverification.review` and nothing else. |

The two rights are independent of the official ones: a reviewer may hold
`verification.review` without `botverification.review`, and vice versa. The panel
hides the nav entry, gates the route and hides the manage-only buttons accordingly;
every route is checked again server-side.

## Manual check

### Telegram Desktop

1. In the panel, open *Third-party verification → Icon catalogue* and add a custom
   emoji document id. The *Emoji* section lists the documents this deployment holds
   with their ids; pick one that a client can actually fetch.
2. Open *Verifiers*, grant `@verifierbot` verifier status with that icon, a company
   name (say `Acme Verification Ltd`), a default description
   (`Verified by Acme`) and `can_modify_custom_description` off for the first pass.
   Confirm the audit entry appeared.
3. Log in to the deployment with official Telegram Desktop and open `@verifierbot`.
   Its profile now carries a **"verified by" block** built from
   `botInfo.verifier_settings` — the company and the icon — while its name has **no**
   platform checkmark. That contrast is the point.
4. Send `/verify`, pick one of your channels from the inline picker, state a reason,
   confirm. `/status` lists the application as pending.
5. In the panel open the queue, open the application, read the *Description the mark
   would carry* preview (with `can_modify_custom_description` off it shows the
   verifier default, not what you asked for), and approve.
6. Within a moment `@verifierbot` messages you the decision.
7. Without restarting the client, check the icon on the approved channel:
   - **Profile** — the icon sits immediately **before** the title, and the
     description line ("Verified by Acme") appears in the profile body
     (`channelFull.bot_verification`).
   - **Dialog list** — the chat row shows the icon before the title.
   - **Message header** — open the chat; the header title carries the icon.
   - **Search** — type the `@username` in global search; the result row carries it.
   - **Invite preview** — from a second account that is **not** a member, open an
     invite link to that channel: the join box carries the icon
     (`chatInvite.bot_verification`).
8. If the peer also holds the platform checkmark, both are visible at once: the
   custom icon before the name, the checkmark after it.
9. In the panel, revoke from the application's danger zone (or *Granted marks →
   Remove mark*). The icon disappears from all of those surfaces on the next push or
   read, and the platform checkmark stays untouched.
10. To check the invisible-badge failure mode on purpose, retire the icon and grant
    a verifier a catalogue entry whose document was deleted: the peer is marked in
    the database and the client draws nothing. That is why the catalogue validates
    documents up front.

### Telegram Android

1. Log in with the official Android client, force-close it and reopen it after the
   grant so the profile cache is cold.
2. `@verifierbot` profile: the verifier block ("verified by *company*" with the
   icon) is rendered under the bot's info, and the bot's name has no checkmark.
3. Approve an application for a **user account** (your own) and open that account's
   profile from a second device: the icon is drawn before the name in the profile
   header and in the chat header, and the description is a line in the profile
   (`userFull.bot_verification`).
4. Chat list and global search rows carry the icon before the name
   (`user.bot_verification_icon`).
5. Custom emoji rendering follows the client's animated-emoji setting: with animated
   emoji disabled the icon shows as a static frame, and while the document is still
   being fetched the slot is briefly empty. Neither is a server-side problem.
6. Revoke from the panel and pull-to-refresh the profile: the icon is gone.

## Limitations

- **A verifier can mark a peer that never asked.** `bots.setCustomVerification`
  authorises the *caller* (the bot itself, or a user who owns it) and the *verifier
  status*, not the target's consent. Verifier status is the trust boundary; that is
  why granting it is an operator-only action, why it has a kill switch, and why
  `TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER` bounds it. A peer cannot refuse or
  remove a mark itself — only the verifier (`/revoke` in the bot dialog, or the RPC
  with `enabled` unset) or an operator can.
- **No per-application event history.** Unlike official verification, there is no
  `*_events` table: an application keeps only `decided_by`, `decision_reason`,
  `internal_note` and its stamps. The full trail lives in the shared
  `admin_commands` / `admin_audit_logs` journal, so the panel's decision page shows a
  decision, not a timeline.
- **A revocation clears `approved_at`.** `0155` pairs each stamp with its status, so
  leaving the approved state nulls the approval stamp. After a revoke, "when was this
  approved?" can only be answered from the audit journal.
- **Applicant notifications are best-effort.** They are sent directly by
  `@verifierbot` after the decision commits, not through a durable outbox like the
  official flow's `verification_notification_outbox`. A delivery failure is logged
  and swallowed (`notifyApplicant`); the decision itself stands, and nothing retries
  the message, so an applicant can end up with a decided application they were never
  told about.
- **Only users and channels can be marked.** `peer_type` is constrained to
  `user`/`channel`, matching the TL surface: legacy basic groups (`chat#…`) have no
  `bot_verification` field in Layer 228, so a non-migrated basic group can never
  show a mark.
- **Only bots can be verifiers, and system bots cannot** — except the built-in
  `@verifierbot`. `botInfo.verifier_settings` exists only on a bot, so a user account
  granted verifier status would carry a status no client can see; seeded service
  accounts are refused outright (`@verifybot` in particular, which owns the *other*
  mechanism).
- **One icon per verifier, one mark per peer.** A verifier cannot vary
  its icon per peer, there are no verification tiers, and no expiry: a mark lives
  until somebody removes it. Nothing re-validates a marked peer over time — losing
  its username or picking up a scam flag later does not clear the mark.
- **A new verifier replaces the current peer mark.** `user.bot_verification_icon`
  and `channel.bot_verification_icon` are single `long` fields, so the database
  stores one matching mark. Replacing it cannot leave an older hidden mark that
  unexpectedly reappears after a revoke or kill-switch change.
- **A retired icon keeps rendering on existing marks.** Retiring a catalogue entry
  only blocks new grants, because the mark copied the document id at grant time.
  Blanking an already-granted icon means revoking the marks (or the verifier).
- **An unresolvable document is an invisible badge.** The catalogue validates the
  document when the entry is written, not continuously. A document deleted afterwards
  leaves marks that render as nothing, and the server has no way to notice.
- **The live push only reaches online sessions**, and the user fan-out is bounded by
  the same capped moderation audience the scam/fake flags use. Everybody else
  converges on their next authoritative read, which is always correct: the icon is an
  overlay read live at the response boundary rather than a cached read-model column.
- **`updateUser` / `updateChannel` carry no `pts`.** They are not persisted as
  message-box events, so a session that was offline during the decision never replays
  the push; it picks the mark up as *state* on its next read, not as an *event*.
- **`channelFull`'s bot-info cache is per process.** `NotifyPeerBotVerification`
  drops it on the instance that handled the decision. On other instances a cached
  `channelFull.bot_info` can still carry a stale `verifier_settings` block (the
  company/icon *of the verifier bot*, not the peer's mark) until that entry expires.
  The peer's own `bot_verification` fields are overlaid post-cache and are not
  affected.
- **`TELESRV_BOT_VERIFICATION_ENABLED=false` is not a badge switch.** It refuses new
  mutations; the marks already granted keep being projected. Use the per-verifier kill
  switch, or revoke, to actually clear badges.
