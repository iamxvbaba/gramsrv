# grammystore

grammY-based Telegram authentication and store bot for ShuzaGram. Runs
independently from the main MTProto server; a bot outage cannot stop MTProto.

## Functionality

- automatic persistent number on `/start` (random mode);
- real-number mode: users bind their actual Telegram phone via contact sharing;
- delivery and storage of real login codes through authenticated `POST /code`;
- free replacement numbers and paid anonymous `+888` numbers (random mode);
  repeat `+888` purchases replace the previous number at an admin-configured discount;
- Premium, server Stars and collectible username purchases through Telegram Stars;
- arbitrary Stars invoices, payment deduplication and a durable sales journal;
- compensated refunds that revoke the exact Stars, Premium entitlement,
  collectible username or paid number before returning Telegram Stars;
- account target IDs and three recent recipients;
- daily bonuses, referrals and a weighted wheel;
- promo codes and button-based giveaways;
- support tickets;
- complete Russian and English localization;
- per-user language and notification settings;
- owner-only statistics, broadcasts, Stars/Premium/bonus grants, invoices,
  payment refunds, login-code access, support replies, Stars-rate and store item
  price controls, repeat-purchase discount and number binding for any user;
- admin exact lookup by Telegram ID or phone number (no data dumps);
- optional required-channel membership gate;
- optional SOCKS5 proxy for Telegram API (for regions where Telegram is blocked).

## Prerequisites

- Docker and Docker Compose v2+
- A Telegram Bot token (from @BotFather)
- A gramsrv Admin API instance with a bearer token
- PostgreSQL (managed by Docker Compose)

## Development setup

### 1. Clone the repository

```bash
git clone https://github.com/iamxvbaba/gramsrv.git
cd gramsrv
```

### 2. Copy the grammystore directory

The bot lives under `cmd/bots/grammystore/`. All files needed for
standalone deployment are already in this directory:

```text
cmd/bots/grammystore/
  src/              # Application source code
  db/init.sql       # PostgreSQL schema
  test/             # Test suite
  Dockerfile        # Container image
  docker-compose.yml       # Production (GHCR image)
  docker-compose-dev.yml   # Development (local build)
  .env.example      # Environment variable template
  package.json      # Node.js dependencies
```

### 3. Create your `.env` file

```bash
cd cmd/bots/grammystore
cp .env.example .env
nano .env
```

Fill in at minimum: `BOT_TOKEN`, `OWNER_IDS`, `GRAMSRV_TOKEN`,
`CODE_WEBHOOK_SECRET`, `POSTGRES_PASSWORD`.

### 4. Start the development stack

```bash
docker compose -f docker-compose-dev.yml --env-file .env up -d --build
```

This builds the image locally and starts PostgreSQL + the bot.

### 5. View logs

```bash
docker compose -f docker-compose-dev.yml logs -f grammystore
```

### 6. Run tests (without Docker)

```bash
npm install
npm test
```

DB tests require a running PostgreSQL instance. They are skipped when
`DATABASE_URL` is not set.

## Installation (production)

```bash
sudo mkdir -p /opt/grammystore
cd /opt/grammystore
```

Create `.env` from the template:

```bash
curl -o .env https://raw.githubusercontent.com/iamxvbaba/gramsrv/main/cmd/bots/grammystore/.env.example
nano .env
```

### Required environment variables

| Variable | Description |
|---|---|
| `BOT_TOKEN` | Telegram Bot API token from @BotFather |
| `OWNER_IDS` | Comma-separated Telegram user IDs for admin access |
| `GRAMSRV_TOKEN` | Bearer token for the gramsrv Admin API |
| `PUBLIC_BASE_URL` | Public base URL for the gramsrv instance |
| `CODE_WEBHOOK_SECRET` | HMAC secret for OTP webhook verification (min 24 chars) |
| `POSTGRES_PASSWORD` | PostgreSQL password (set a strong value) |
| `BOT_MODE` | `random` or `real` — controls number generation mode |

All required values must not be empty or contain placeholder values such as
`CHANGE_ME`, `YOUR_*`, `<...>`, or `example`. The application refuses to start
if any required variable is missing or still contains a placeholder.

### SOCKS5 proxy (for Russia and other blocked regions)

Telegram API is blocked in Russia. Configure a SOCKS5 proxy to route bot
API traffic through an accessible server:

| Variable | Description |
|---|---|
| `TELEGRAM_PROXY_URL` | SOCKS5 proxy URL, e.g. `socks5h://host:1080` |
| `TELEGRAM_PROXY_USERNAME` | Proxy username (if authentication is required) |
| `TELEGRAM_PROXY_PASSWORD` | Proxy password (if authentication is required) |

Use `socks5h://` (with `h`) for remote DNS resolution — the proxy resolves
`api.telegram.org` to an IP, avoiding local DNS blocking. Use `socks5://`
for local DNS resolution (the client resolves the hostname before connecting).

Example `.env`:

```text
TELEGRAM_PROXY_URL=socks5h://your-proxy-host:1080
TELEGRAM_PROXY_USERNAME=myuser
TELEGRAM_PROXY_PASSWORD=mypassword
```

If `TELEGRAM_PROXY_URL` is empty or unset, the bot connects directly to
Telegram without a proxy.

### Bot modes

**Random mode** (`BOT_MODE=random`, default):
- Users receive a randomly generated phone number on `/start`.
- Each Telegram user owns exactly one number at a time: requesting a free
  replacement releases the previous free number, and buying a `+888` replaces
  the current number. A repeat `+888` purchase retires the old `+888` (it is
  never recycled) and rebinds the server account to the new number.
- Free-number reservations are limited to 10 per user; retired numbers are not recycled.
- Numbers persist across bot restarts.

**Real mode** (`BOT_MODE=real`):
- Users must bind their actual Telegram phone number via contact sharing.
- Random number generation is rejected server-side.
- The bound phone is stored in PostgreSQL and survives restarts.

In both modes, purchasing a number only reserves it and atomically records the
allocation, sale and completed payment. Ordinary user flows never call the server
set-phone API; the server account phone changes only on purchase/retention rebind
or the owner's explicit number-binding command. Existing real-phone and free
routes are retained until they are replaced.

The shop sells numbers only — there is no buyback and no sell-offer flow. A user
who already owns a `+888` sees "Buy a new number" and pays a repeat-purchase
discount, set by the owner as `number_discount_percent` via the prices panel
(effective price `max(1, round(base × (100 − discount) / 100))`). Number purchases
atomically record the allocation, sale and completed payment.
Free-number re-rolls are capped per UTC day by the admin-set setting
`free_number_daily_limit` (0 = unlimited), applied with the `free N` command from
the prices panel; the counter resets on a fresh day.
For a stored number payment interrupted by a database/process failure, an owner
can use `/retry_payment <charge_id>` without charging the customer again.
Before refunding a number, change away from it in the client and wait for issued
verification codes to expire. The bot requires a successful server lookup showing
no account still uses it. Refunded numbers are retired permanently, never recycled.
See [number lifecycle and verification invariants](NUMBER_LIFECYCLE.md).

Schema upgrades are applied automatically: the container runs `npm start`,
which first applies every file in `db/migrations/` (001 through 007) against
the configured database before starting the bot. Each migration is idempotent,
so running all of them on every boot is safe — on a fresh install created from
`db/init.sql` they are no-ops, on an existing install they close any schema
gap. Back up the database before upgrading. Migrations do not repair unsafe
pre-release ownership state.
Local images use an allowlisted build context (`.dockerignore`); `.env` is
provided only at runtime.

### Production deployment (GHCR image)

```bash
docker compose pull
docker compose up -d
```

The production `docker-compose.yml` pulls the pre-built image from
`ghcr.io/iamxvbaba/gramsrv/grammystore:main`. No local build is required.

### Development deployment (local build)

```bash
docker compose -f docker-compose-dev.yml --env-file .env up -d --build
```

The development compose file builds the image locally from the Dockerfile.

## Management

```bash
# Start
docker compose up -d

# Stop
docker compose down

# Restart
docker compose restart

# View logs
docker compose logs -f grammystore

# Pull latest production image
docker compose pull
docker compose up -d

# Check health
curl http://localhost:2800/healthz
```

## PostgreSQL persistence

All data is stored in a PostgreSQL container with a named volume (`pgdata`).
The schema is initialized automatically from `db/init.sql` on first start.

Back up the database:

```bash
docker compose exec postgres pg_dump -U grammystore grammystore > backup.sql
```

Restore:

```bash
docker compose exec -T postgres psql -U grammystore grammystore < backup.sql
```

## Login-code webhook

Configure gramsrv's code-delivery webhook for:

```text
TELESRV_PHONE_CODE_DELIVERY_PROVIDER=webhook
TELESRV_OTP_WEBHOOK_URL=http://grammystore:2800/v1/otp/deliveries
TELESRV_OTP_WEBHOOK_SECRET=<same value as CODE_WEBHOOK_SECRET>
```

The endpoint verifies the HMAC-SHA256 signature, a five-minute timestamp
window and `Idempotency-Key`. It returns HTTP 202 immediately; Telegram
delivery then runs asynchronously. `/healthz` is read-only.

## Admin lookup

Administrators can look up specific codes/numbers through the admin panel:

1. Open the admin panel (`/admin`).
2. Press "Lookup".
3. Enter a phone number (with `+`) or a Telegram ID.

The lookup returns only the data for that exact query. There is no command
that dumps or lists all stored codes or numbers.

## Container image

The GHCR image coexists with the main server images under the same package:

- `ghcr.io/iamxvbaba/gramsrv/server`
- `ghcr.io/iamxvbaba/gramsrv/admin`
- `ghcr.io/iamxvbaba/gramsrv/grammystore`

All three images are published from the same repository.
