# OTP Webhook example

This command implements the `telesrv` OTP Webhook v1 receiving side with only
the Go standard library. It validates the signed request, rejects expired or
invalid payloads, and deduplicates concurrent or repeated delivery IDs.

The default `exampleDelivery` function deliberately does **not** send a real
email/SMS and does not print the code or recipient. For local debugging only,
set `TELESRV_OTP_EXAMPLE_LOG_CODE=true` to print the received code while still
redacting the recipient. Replace that one function with the API call for your
email or SMS provider before real use.

## Run

```powershell
$env:TELESRV_OTP_EXAMPLE_SECRET = 'replace-with-a-random-secret'
$env:TELESRV_OTP_EXAMPLE_LOG_CODE = 'true' # local testing only
go run ./cmd/otpwebhook-example
```

The default endpoints are:

- `POST http://127.0.0.1:2800/v1/otp/deliveries`
- `GET http://127.0.0.1:2800/healthz`

Then configure `telesrv` with the same secret:

```dotenv
TELESRV_EMAIL_CODE_DELIVERY_PROVIDER=webhook
TELESRV_PHONE_CODE_DELIVERY_PROVIDER=webhook
TELESRV_OTP_WEBHOOK_URL=http://127.0.0.1:2800/v1/otp/deliveries
TELESRV_OTP_WEBHOOK_SECRET=replace-with-a-random-secret
```

The example accepts these optional settings:

```dotenv
TELESRV_OTP_EXAMPLE_ADDR=127.0.0.1:2800
TELESRV_OTP_EXAMPLE_MAX_SKEW=5m
TELESRV_OTP_EXAMPLE_LOG_CODE=false
```

The idempotency registry is intentionally in memory. A production receiver
must put delivery IDs and the downstream provider message ID in durable shared
storage before running more than one instance or surviving restarts. Pass the
same delivery ID to a downstream provider when it supports idempotency. The
example remembers both successful and failed outcomes until the code expires,
because an apparent downstream failure may have happened after it sent the
message.
