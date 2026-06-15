# Upstream Sync Log

This file tracks manual syncs from the private `telesrv` workspace into the public
`gramsrv` repository. Keep it append-only so future syncs can start from the last
confirmed source checkpoint.

## Current Checkpoint

- Last synced source commit: `06b6ae2bb2f90bd7cc1d6a8404a82aff507e6821`
- Last synced target commit: `b435784`
- Sync date: 2026-06-16
- Source path used locally: `D:\work\waikuai\telegram\telesrv`
- Target path used locally: `D:\work\waikuai\telegram\gramsrv-public`

## Sync History

| Date | Source commit | Target commit | Status | Notes |
|---|---|---|---|---|
| 2026-06-09 | `488e409a1898e9c739cc0bd24cb9791636dfd6b3` | `23a2b2a` | synced | Restored sticker path placeholders, media in dialogs/history, media forwarding to channels, and channel history pagination fixes. Source README changes were already covered by the public repo's gramsrv README wording, so no README file changed in the target cherry-pick. |
| 2026-06-09 | `07b2497664bd108dec84f6cfe43715540faf2688` | `6fd690a` | synced | Kept admin/ban participant changes as transient `updateChannelParticipant` pushes instead of durable channel pts events; added memory/Postgres regression coverage. |
| 2026-06-10 | `c65f76f56278f74082c4fa792ed49104d5d33c38` | `d84fa6e` | synced | Aligned private and channel read/update semantics, including content unread state, block-aware private sends/forwards, channel difference nudges, and durable read-content events. |
| 2026-06-10 | `c0a0e5b52240ed415d3b43ba77659821887bf50b` | `860e581` | synced | Added viewer-specific user projection, contact accept/phone sharing behavior, and regression coverage for contact/user/message projections. |
| 2026-06-14 | `a636192ef22ee69d792b0ca7db1c6be963be9cb2` | `8ff3343` | synced | Added privacy-aware user projection, account privacy persistence/service, profile/fallback photo kinds, and viewer-specific dialog/message/user projections. |
| 2026-06-14 | `77b033c8bf8c0a76ff0d7065e2192cbe55d3a3b6` | `75a8861` | synced | Exposed account privacy RPCs and profile/contact photo RPCs, plus `users.getFullUser` privacy and photo projection updates. |
| 2026-06-16 | `04f4527df32ad5c35720cccc41d27fe51549612f` | `6dc4294` | synced | Completed account authorization flows, including active sessions, authorization reset, 2FA/SRP password settings, recovery paths, and auth error mapping. |
| 2026-06-16 | `06b6ae2bb2f90bd7cc1d6a8404a82aff507e6821` | `b435784` | synced | Added Android legacy startup compatibility for MTProto exchange replay/acks and allowlisted legacy `langpack.getLanguages` handling. |

## Next Sync

Start the next batch from source commits after `06b6ae2bb2f90bd7cc1d6a8404a82aff507e6821`.
At the time this log was created, newer `telesrv` commits existed after that point and were
intentionally left out because the latest batch was scoped to the next two commits after
`77b033c8bf8c0a76ff0d7065e2192cbe55d3a3b6`.
