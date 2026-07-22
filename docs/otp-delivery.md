# OTP delivery providers

`telesrv` owns OTP generation, storage, attempt limits, expiry, verification,
and consumption. A delivery provider receives an already-issued code and must
only deliver it. It must not generate a replacement code or decide whether an
authentication attempt succeeds.

## Routing

- `TELESRV_PHONE_CODE_DELIVERY_PROVIDER=development` preserves the local fixed
  code. `webhook` generates random SMS codes for login, registration,
  login-email reset fallback, and phone changes.
- `TELESRV_EMAIL_CODE_DELIVERY_PROVIDER=smtp` preserves direct SMTP delivery.
  `webhook` handles login-email, login-email setup, and login-email change.
- One Webhook endpoint may handle both channels. `channel` and `purpose` in the
  request select the downstream template/provider.

For an existing account, external delivery is additive: `auth.sendCode` and
`auth.resendCode` first commit the same code as a durable incoming message from
777000, then invoke the configured SMS or login-email provider. A provider
cannot replace or invalidate that App-code. A new phone and email setup/change
have no existing login dialog to receive the code, so those flows use only the
configured external provider.

## Webhook v1 request

`telesrv` sends one `POST` request and does not follow redirects:

```http
POST /v1/otp/deliveries HTTP/1.1
Content-Type: application/json
Accept: application/json
Idempotency-Key: otp_0193f0...
X-Telesrv-Timestamp: 1784275200
X-Telesrv-Signature: sha256=...
```

```json
{
  "version": "1",
  "delivery_id": "otp_0193f0...",
  "purpose": "login_email",
  "channel": "email",
  "recipient": "alice@example.test",
  "code": "482913",
  "expires_at": "2026-07-17T16:05:00Z",
  "expires_in": 299,
  "locale": "zh-CN"
}
```

Current purpose values are `login_email`, `login_sms`,
`login_email_setup`, `login_email_change`, and `change_phone`. Current channel
values are `email` and `sms`.

`delivery_id` is an opaque idempotency key. Replays of the same ID must not
send a second message. A resend that creates a new code has a new delivery ID.

When `TELESRV_OTP_WEBHOOK_SECRET` is non-empty, the signature is lowercase hex
HMAC-SHA256 over:

```text
<X-Telesrv-Timestamp>.<exact raw JSON request body>
```

## Response

An accepted request returns any 2xx response with this JSON shape:

```json
{
  "accepted": true,
  "message_id": "provider-message-123"
}
```

`204 No Content` is also accepted. Other 2xx responses must explicitly contain
`"accepted": true`; a missing or malformed acknowledgement is treated as an
unknown outcome because the provider may already have sent the code.

An explicit rejection may use either a non-2xx status or `accepted: false`:

```json
{
  "accepted": false,
  "error_code": "RECIPIENT_INVALID",
  "retryable": false
}
```

The response body is capped at 64 KiB. For a flow without a durable 777000
fallback, an explicit rejection invalidates only the code attempt that
triggered that request. A transport error or invalid successful acknowledgement
preserves the code and returns its hash because the provider may already have
sent it. For an existing-account login, any provider failure is reported but
does not fail the RPC or invalidate the code: the durable 777000 copy remains
the authoritative fallback.

Webhook logs contain the opaque delivery ID, purpose, channel, status, and
transport error only. The code and recipient are not logged.

A runnable standard-library receiver is available at
[`cmd/otpwebhook-example`](../cmd/otpwebhook-example/README.md). It includes
signature/timestamp validation, request limits, idempotency, health checking,
and graceful shutdown. Its delivery function is intentionally a no-op adapter
and must be replaced with the user's email/SMS API call.
