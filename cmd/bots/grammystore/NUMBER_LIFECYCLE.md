# Number allocation and client-confirmed phone changes

Status: current design for the PostgreSQL store bot.

## Authorization boundary

The bot reserves numbers and delivers server-issued verification codes. It never
calls the administrative set-phone API, including from owner commands. Manual
account IDs and phone-to-ID lookups select purchase/gift recipients; they do not
authorize an account mutation. A Telegram contact establishes a code delivery
route, not an authenticated session on the server.

An existing account changes its phone in its signed-in client using
`account.sendChangePhoneCode` and `account.changePhone`. The server checks the
current user/auth key and verification code. This bot does not implement or change
MTProto, session, update/outbox, or PTS behavior.
Protocol reference: [account.changePhone](https://core.telegram.org/method/account.changePhone).

## Persisted invariants

- A phone is allocated to at most one bot owner, including historical and
  retired numbers. Retired numbers are never returned to the random pool.
- Exactly one non-retired number may be current for an owner. Earlier free
  numbers remain allocated and continue receiving codes: selecting a new number
  is not evidence that the account has already changed its phone.
- Buying a number does not remove a verified real-phone route.
- Free-number reservations are capped at 10 per owner; reaching the cap rejects
  allocation without releasing an old phone or changing its route.
- Number allocation, the sale snapshot, and payment completion commit in one
  PostgreSQL transaction. Charge identity (buyer, payload, amount) is immutable.
  A retry reuses the recorded sale and cannot allocate a second number.
- A database error rolls the entire number purchase back. A completed purchase
  is not marked failed if a later notification fails. An owner can retry a stored
  number payment using `/retry_payment <charge_id>`; no new payment is created.
- Allocation locks the owner, including on first allocation. Only collisions on
  the unique phone key are retried; other database errors propagate.

## Refund boundary

The user first changes away from the purchased number in their signed-in client.
The bot refuses retirement while a previously delivered code is unexpired or the
server still reports any account using the number. The server lookup is mandatory,
read-only and timeout-bounded; errors/malformed replies fail closed. The number row
is locked across checks and retirement; OTP acceptance uses the same row lock.
No intervening webhook can accept a fresh code during retirement. The maximum
recorded code expiry never shrinks.

Once safe, the number becomes a permanent retired reservation, its code-access
grants are removed, and the previous free number becomes current if present.
Retirement is idempotent across a crash before recording refund progress. If
Telegram's refund fails, retrying cannot revoke another number or repeat a
completed internal reversal. Retired numbers reject future OTP deliveries even
if a later owner command creates a code-access grant.

Production requires the server's random-code webhook delivery path for these
numbers; fixed development codes or a second independent provider are not a
supported custody boundary. Do not purge historical numbers or code expiry state.

## Verification matrix

| Scenario | Required result |
|---|---|
| Another account ID followed by start/replace/buy | No server phone mutation |
| New number or +888 purchase | Old free/real-phone OTP routes remain |
| Collision followed by an available phone | Retry in a valid transaction |
| Generator exhaustion / SQL failure during sale | Atomic rollback |
| Charge replay / concurrent replay / restart | One number, sale and completed payment |
| Replay with changed buyer/payload/amount | Conflict without writes |
| Refund while bound, codes active, or API unavailable | No reversal or Telegram refund |
| OTP concurrent with refund | Serialized; active-code check cannot be bypassed |
| Refund / Telegram failure then retry | Exact number retired once, never reallocated |
| Delayed OTP for retired number | Explicit rejection, no recipient |
| Docker build beside .env / local dependencies | Only runtime source/manifests included |

Fresh installations use `db/init.sql`. Existing PostgreSQL installations must
apply `db/migrations/001-number-retirement.sql` before starting this version.
The migration adds the retirement field and constraint; it does not infer ownership
or repair state written by an unsafe pre-release bot.
