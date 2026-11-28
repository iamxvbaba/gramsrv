# Push-уведомления (FCM / Web push / Bark / ntfy)

Статус: **FCM, Web push, Bark и ntfy реализованы и проверены в проде**; APNs
спроектирован, но отложен (Bark/ntfy закрывают iOS без сертификата Apple у
оператора). Issue:
[#98](https://github.com/iamxvbaba/gramsrv/issues/98). Этот документ — проект,
который просил автор issue; он синхронизирован с реализацией.

Где что лежит:

- Хранилище токенов: `internal/domain/push_token.go`, `internal/store/push_token.go`,
  `internal/store/memory/push_token.go`, `internal/store/postgres/push_token.go`,
  миграция `deploy/migrations/*_device_push_tokens.up.sql`.
- RPC: `internal/rpc/account.go` (`onAccountRegisterDevice` /
  `onAccountUnregisterDevice`), `internal/rpc/deps.go` (`Deps.PushTokens`).
- Шифрование: `internal/push/cipher.go` (push-шифрование MTProto v2).
- FCM: `internal/push/fcm.go` (HTTP v1 + OAuth2 сервисного аккаунта).
- Web push: `internal/push/webpush.go` (RFC 8291/8188 aes128gcm + RFC 8292 VAPID).
- Bark: `internal/push/bark.go`; ntfy: `internal/push/ntfy.go`.
- payload/service: `internal/push/service.go` (диспетчеризация по типу токена).
- Офлайн-триггер и mute/badge: `internal/rpc/offline_push.go`,
  `internal/rpc/offline_push_payload.go` и хук `WithOfflinePusher` в
  `internal/rpc/outbox_dispatcher.go`.
- Сборка/конфиг: `cmd/telesrv/main.go`, `internal/config/config.go`.

## 1. Обзор

Без push `account.registerDevice` просто принимается и отбрасывается, и фоновый
клиент не получает ничего, пока не переподключится. Функция добавляет
**необязательную, по умолчанию выключенную** доставку:

- `account.registerDevice`/`unregisterDevice` реально сохраняют токен устройства и
  клиентский `secret`, когда подключено хранилище. Токен уникален по
  `(token_type, token)` и переприсваивается последнему аккаунту; пустой токен даёт
  `TOKEN_EMPTY`.
- Durable-outbox и так знает, сколько живых сессий достигла правка. Когда их **ноль**,
  `OfflinePushBridge` строит `loc_key`-уведомление из правок «в перспективе смотрящего»,
  схлопывает по пользователю и отправляет из пула воркеров, не блокируя outbox.
  Онлайн-доставка не меняется.
- Mute: читается `mute_until` собеседника (scope peer плюс категории
  users/chats/broadcasts). Для замьюченного собеседника вместо видимого уведомления
  отправляется **ограниченный по частоте `MESSAGE_MUTED`** (обновление бейджа); токен с
  `no_muted` в этом обновлении пропускается.
- Инвалидация: ответ `UNREGISTERED`/`404`/`410` удаляет токен.

## 2. Шифрование

**FCM (token_type 2)**: JSON уведомления запечатывается клиентским `secret` по схеме
MTProto v2, которую используют официальные клиенты, и отправляется как поле `p` в
data-only FCM-сообщении:

```
blob       = SHA1(secret)[-8:] || msg_key(16) || AES-IGE(padded_plaintext)
msg_key    = SHA256(secret[96:128] || padded_plaintext)[8:24]
aes key/iv = MessageKeyData(secret, msg_key, incoming=true)  // x=8
padded     = int32LE(len(json)) || json || случайный паддинг до 16 байт
p          = base64url(blob)
```

Схема сверена с референсными реализациями (DrKLO Android `PushListenerController` и
`MessageKeyData`, `pushSingleManager` в Telegram Web K) и в тестах проверяется
независимым клиентским дешифратором.

**Web push (token_type 10)**: токен подписки — JSON (`endpoint` + `p256dh`/`auth`).
JSON уведомления шифруется по **RFC 8291** (`aes128gcm`, **RFC 8188**) и отправляется с
ES256 `Authorization` по **RFC 8292** VAPID. Проверено тест-вектором RFC 8291 §5 и
сквозным тестом, который валидирует VAPID и расшифровывает тело.

## 3. Bark и ntfy (token_type 14/15)

Без сертификата Apple iOS можно доставлять через два самостоятельных HTTP-сервиса, каждый
показывает уведомление своим приложением (у него уже есть своё APNs-право):

- **Bark** (`internal/push/bark.go`, `TELESRV_PUSH_BARK_SERVER`): `POST {server}/push`
  с `{device_key,title,body,level,sound,group,url}`. Токен — ключ устройства Bark или
  JSON `{"endpoint","key","sound","group","url"}`.
- **ntfy** (`internal/push/ntfy.go`, `TELESRV_PUSH_NTFY_SERVER`): `POST {server}/{topic}`
  с `{title,message,priority,click,tags}` и необязательным `Authorization: Bearer`.
  Токен — topic или JSON `{"endpoint","topic","token","click"}`.

Оба используют тот же `Title`/`Description`, что и web push; `MESSAGE_MUTED` уходит
пассивно/с низким приоритетом, без звука. Оба типа токенов — расширения telesrv: патченный
клиент регистрирует Bark-ключ / ntfy-topic под соответствующим типом. Пустой адрес
сервера отключает провайдера.

## 4. Конфигурация (всё опционально)

| Настройка | По умолчанию | Значение |
|---|---|---|
| `TELESRV_PUSH_ENABLED` | `false` | Мастер-выключатель; при `false` `registerDevice` — no-op и токены не хранятся. |
| `TELESRV_PUSH_ALWAYS` | `false` | Публиковать каждое новое сообщение даже при живой сессии (онлайн-клиент получит и обновление в приложении, и push). |
| `TELESRV_PUSH_FCM_SERVICE_ACCOUNT_FILE` | `` | Путь к JSON сервисного аккаунта Firebase (файловый секрет). |
| `TELESRV_PUSH_FCM_PROJECT_ID` / `_TOKEN_URL` / `_SCOPE` | — | Переопределения. |
| `TELESRV_PUSH_WEBPUSH_VAPID_PUBLIC_KEY` / `_PRIVATE_KEY` | `` | Пара VAPID (base64url). |
| `TELESRV_PUSH_WEBPUSH_SUBJECT` / `_TTL` | — / `86400` | VAPID `sub`, TTL. |
| `TELESRV_PUSH_BARK_SERVER` | `` | Адрес Bark; пусто — Bark выключен. |
| `TELESRV_PUSH_BARK_SOUND` / `_GROUP` | `` | Звук/группа Bark по умолчанию. |
| `TELESRV_PUSH_NTFY_SERVER` | `` | Адрес ntfy; пусто — ntfy выключен. |
| `TELESRV_PUSH_NTFY_TOKEN` | `` | Необязательный Bearer-токен ntfy. |
| `TELESRV_PUSH_DEBOUNCE` / `_SEND_TIMEOUT` | `30s` / `5s` | Окно схлопывания / таймаут HTTP. |

Новых зависимостей нет: JWT сервисного аккаунта и VAPID — только на стандартной
библиотеке (`crypto/ecdh`, `crypto/ecdsa`, `crypto/aes`, HKDF на `crypto/hmac`).

## 5. Требования к клиенту (неизбежно)

- **Android** нужно пересобрать под собственный проект Firebase (замена
  `google-services.json`), иначе FCM не доставит на сервисный аккаунт сервера.
- **Web** нужно собрать с VAPID-ключом, чья приватная половина есть у сервера
  (`VITE_PUSH_SERVER_KEY`); сервис push отклонит несовпадающий ключ.
- **iOS**: можно использовать приложения Bark/ntfy (§3) без сертификата Apple; нативный
  APNs — отдельная задача (`p8`-ключ, `api.push.apple.com` HTTP/2, `mutable-content`).

## 6. Тесты

- `internal/push`: FCM против `httptest` (кэш токена, инвалидация), round-trip
  MTProto-шифра, тест-вектор RFC 8291, сквозная проверка VAPID+расшифровки, а также
  отправка/диспетчеризация Bark и ntfy.
- `internal/rpc`: регистрация/хранение, «push только при `sent == 0`», mute →
  `MESSAGE_MUTED`, пропуск `no_muted`.
- `internal/store`: memory + postgres (`*_integration_test.go`, нужен DSN).

## 7. Пока не входит

- Нативный APNs (нужны ключ Apple и пересборка iOS).
- Push для scope топиков форума сверх значения по умолчанию.
