# Админ-панель: все пути и контракты API

Документация по всем путям встроенной админ-панели `telesrv` — адресам
страниц (SPA) и API-маршрутам, которые за ними стоят. Сервер панели живёт в
`cmd/telesrv-admin`, маршруты объявлены в функции `(*server).routes()`
(`server.go:50`).

Панель — это обычный HTTP-сервер: он отдаёт собранный фронтенд (`/`) и JSON-API
под префиксом `/api/`. Все API-пути требуют сессию (cookie), а все
изменяющие запросы — ещё и CSRF-токен. Многие маршруты дополнительно проверяют
права оператора (`permission`).

## Как пользоваться этим документом

- Для каждого GET-эндпоинта указаны **параметры запроса** (query) и краткая
  форма **ответа**.
- Для каждого POST-эндпоинта в блоке `/api/actions/*` и решений верификации
  указано **тело запроса** (JSON). Все они разделяют общие поля
  (см. ниже) плюс свои специфичные поля.
- `int64` в JSON можно передавать и как число, и как строку (например,
  `"user_id": "123"` или `"user_id": 123`) — сервер принимает оба варианта
  (`flexInt64`). `flexUnix` принимает Unix-секунды или дату `2006-01-02` /
  RFC3339.
- Все изменяющие запросы идут в Admin API. Ответ команды — либо
  `{"status":..., "message":..., "command_id":...}` при успехе (HTTP 200), либо
  `{"error":..., "code":...}` при ошибке. Конфликт оптимистичной блокировки —
  `409`.

## Аутентификация и сессия

| Метод | Путь | Описание |
| --- | --- | --- |
| POST | `/api/login` | Вход. Тело: `{"secret": "..."}`. Проверяет секрет из конфига, выдаёт подписанную cookie-сессию и cookie CSRF. Единственный изменяющий маршрут без CSRF-токена; проверяется только Origin. |
| POST | `/api/logout` | Выход. Уничтожает сессию и cookie CSRF. |
| GET | `/api/session` | Возвращает `{"actor": "...", "permissions": [...], "csrf_token": "..."}`. Панель вызывает его при старте. |

Сессия живёт в подписанной cookie (TTL по умолчанию 12 часов), серверного
хранилища сессий нет. Все запросы, кроме `GET/HEAD/OPTIONS` и `/api/login`,
обязаны предъявить:

- cookie `telesrv_admin_csrf`;
- заголовок `X-CSRF-Token` с тем же значением;
- Origin, совпадающий с хостом (если Origin присутствует).

Нарушение любого из трёх условий — `403 Forbidden`.

## Права и их проверка

Панель различает обычную авторизацию (`requireAuthAPI`) и проверку права
(`requirePermission`). Имена прав совпадают со строками в
`TELESRV_ADMIN_UI_PERMISSIONS`:

| Право | Что даёт |
| --- | --- |
| `*` | Все права (wildcard). |
| `premium.manage` | Управление каталогом Premium-планов, начисление Premium. |
| `bots.token.read` | Экспорт токена бота (`/api/actions/export-bot-token`). |
| `verification.review` | Чтение и решение очереди официальной верификации. |
| `verification.revoke` | Снятие официального бейджа (в дополнение к `verification.review`). |
| `botverification.review` | Чтение и решение очереди сторонней (ботовой) верификации. |
| `botverification.manage` | Назначение верификаторов, редактирование каталога иконок, снятие метки сторонней верификации. |

Права официальной и сторонней верификации намеренно независимы. Список прав
сессии возвращается в `/api/session`, чтобы интерфейс скрывал недоступные
разделы.

## Страницы интерфейса (SPA)

SPA-роуты декларативны: любое имя раздела отдаётся сервером как `/` (index.html),
а сам фронтенд решает, что рендерить (`web/src/pages/Routes.tsx`).

| Путь | Страница |
| --- | --- |
| `/` | Панель управления (счётчики, статистика хранилища, ссылки на разделы). |
| `/accounts` | Список аккаунтов (с поиском). |
| `/accounts/{id}` | Карточка аккаунта: профиль, действия. |
| `/channels` | Список супергрупп и каналов. |
| `/channels/{id}` | Карточка канала. |
| `/bots` | Список ботов. |
| `/bots/{id}` | Карточка бота. |
| `/broadcasts` | Рассылки. |
| `/monetization`, `/premium` | Звёзды и Premium: планы, начисление. Требует `premium.manage`. |
| `/moderation` | Жалобы и модерация: список кейсов. |
| `/moderation/{id}` | Детали кейса модерации. |
| `/emoji` | Каталог emoji-наборов. |
| `/stickers` | Каталог стикерпаков. |
| `/gif-catalog` | Каталог GIF. |
| `/messages`, `/messages/private` | Аудит личных сообщений. |
| `/messages/detail`, `/messages/private/detail` | Детали личного сообщения (`?owner_user_id=&msg_id=`). |
| `/messages/groups` | Аудит сообщений групп/каналов. |
| `/messages/groups/detail` | Детали сообщения в группе (`?channel_id=&msg_id=`). |
| `/gifts` | Звёздные подарки: каталог, коллекционки, аукционы. |
| `/give-gifts` | Выдача подарков. |
| `/collectible-usernames` | Коллекционные юзернеймы. |
| `/collectible-usernames/{id}` | Карточка коллекционного юзернейма. |
| `/collectible-phones` | Анонимные номера. |
| `/account-ratings` | Рейтинг аккаунтов. |
| `/account-ratings/{user_id}` | Карточка рейтинга аккаунта. |
| `/storage` | Объектное хранилище: статистика. |
| `/verification` | Официальная верификация: очередь заявок. Требует `verification.review`. |
| `/verification/{id}` | Детали заявки на официальную верификацию. Требует `verification.review`. |
| `/bot-verification` | Сторонняя верификация: верификаторы, иконки, метки, очередь. Требует `botverification.review`. |
| `/bot-verification/{id}` | Детали запроса сторонней верификации. Требует `botverification.review`. |

Несуществующий API-путь возвращает `404 {"error":"api route not found"}`; любой
неизвестный путь фронтенда отдаётся как `/`.

## Соглашения ответов

- Успешные чтения — `200` + JSON; файлы (аватары, анимации, превью) — сам файл.
- Ошибки — JSON вида `{"error": "...", "code": "..."}` с соответствующим HTTP-статусом.
- `401` — нет/просрочена сессия; `403` — нарушение CSRF/Origin или не хватает права
  (в `requirePermission` к телу добавляется поле `permission`);
  `409` — конфликт оптимистичной блокировки (кейс модерации, верификация);
  `502` — Admin API недоступен.
- Ошибки команд, ушедших в Admin API, возвращаются как
  `{"status": ..., "message": ..., "error": ...}`.

## Общие поля команд (POST `/api/actions/*`)

Каждый изменяющий запрос несёт в теле:

| Поле | Тип | Описание |
| --- | --- | --- |
| `command_id` | string | Идемпотентный ключ команды. Повтор с тем же `command_id` не выполняет действие дважды. Если пусто — генерируется сервером. |
| `reason` | string | Обязательная причина операции (аудит). |
| `confirm` | bool | Подтверждение оператором (`true`). |

---

## API: дашборд и хранилище

| Метод | Путь | Описание |
| --- | --- | --- |
| GET | `/api/dashboard` | Сводка: `counts`, `storage`, при наличии — `host`. |
| GET | `/api/storage/stats` | Статистика объектного хранилища. |

## API: аккаунты

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/accounts` | Параметры: `q` (поиск), `before_id` (int64), `before_active_us` (int64, микросекунды), `limit` (int). Ответ: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `next_before_active_us`, `listing`. |
| GET | `/api/accounts/{id}` | Карточка аккаунта: профиль, флаги, статистика (проксируется/читается из БД). |
| GET | `/api/accounts/{id}/avatar` | Аватар аккаунта (файл). |
| GET | `/api/account-ratings` | Параметры: `q`, `min_level` (int), `user_id` (int64), `before_id` (int64), `limit` (int). Ответ: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/account-ratings/{user_id}` | Ответ: `rating`, `events`. |

## API: каналы и супергруппы

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/channels` | Параметры: `q`, `before_id` (int64), `before_updated_us` (int64, микросекунды), `limit` (int). Ответ: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `next_before_updated_us`, `listing`. |
| GET | `/api/channels/{id}` | Карточка канала. |
| GET | `/api/channels/{id}/avatar` | Аватар канала (файл). |

## API: боты и рассылки

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/bots` | Параметры: `q`, `before_id` (int64), `limit` (int). Ответ: `query`, `limit`, `rows`, `has_more`, `next_before_id`, `listing`. |
| GET | `/api/bots/{id}` | Карточка бота. |
| GET | `/api/broadcasts` | Параметры: `before_id` (int64), `limit` (int). Ответ: `limit`, `rows`, `has_more`, `next_before_id`. |

## API: медиа-каталоги

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/emoji` | Параметры: `q`, `before_id` (int64), `limit` (int). Ответ: `query`, `rows`, `has_more`, `next_before_id`, `listing`. |
| GET | `/api/emoji/{id}/animation` | Lottie-анимация emoji (файл). |
| GET | `/api/stickers` | Параметр: `kind` (string, фильтр по типу). Ответ: `rows`, `max_items`. |
| GET | `/api/stickers/{id}/documents` | Ответ: `document_ids`. |
| GET | `/api/stickers/documents/{id}/animation` | Анимация стикера (файл). |
| GET | `/api/gif-catalog` | Ответ: проксируется из Admin API (`/v1/gif-catalog`). |
| GET | `/api/gif-catalog/documents/{id}/preview` | Превью GIF (файл). |

## API: аудит сообщений

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/messages` | Параметры: `owner_user_id` (int64, обязателен с `peer_id`), `peer_id` (int64), `before_date` (int64), `before_id` (int), `limit` (int). Ответ: `owner_user_id`, `peer_id`, `before_date`, `before_id`, `limit`, `rows`. |
| GET | `/api/messages/detail` | Параметры: `owner_user_id` (int64, обяз.), `msg_id` (int, обяз.). Ответ: карточка сообщения. |
| GET | `/api/messages/groups` | Параметры: `channel_id` (int64, обяз.), `before_date` (int64), `before_id` (int), `limit` (int). Ответ: `channel_id`, `before_date`, `before_id`, `limit`, `rows`. |
| GET | `/api/messages/groups/detail` | Параметры: `channel_id` (int64, обяз.), `msg_id` (int, обяз.). Ответ: карточка сообщения. |

## API: звёздные подарки и коллекционки

| Метод | Путь | Параметры / ответ |
| --- | --- | --- |
| GET | `/api/gifts` | Ответ: `Gifts` (список подарков). |
| GET | `/api/auctions` | Ответ: `Auctions` — текущее состояние всех авторских аукционов и запланированных дропов. |
| GET | `/api/official-gifts` | Ответ: проксируется из Admin API (`/v1/official-gifts`). |
| GET | `/api/official-gifts/{id}/animation` | Анимация официального подарка (файл). |
| GET | `/api/gifts/{id}/animation` | Анимация подарка (файл). |
| GET | `/api/gifts/{id}/collectibles` | Ответ: проксируется из Admin API (`/v1/gifts/{id}/collectibles`). |
| GET | `/api/gifts/{id}/collectibles/{kind}/{attribute_id}/animation` | Анимация атрибута коллекционки (файл). `kind` ∈ {`model`, `pattern`}. |
| GET | `/api/collectible-usernames` | Параметры: `status` (`` | `vault` | `owned` | `burned`), `owner_user_id` (int64), `before_id` (int64), `limit` (int), `q`. Ответ: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/collectible-usernames/{id}` | Ответ: `asset`, `transfers`. |
| GET | `/api/collectible-phones` | Параметры пробрасываются в Admin API как есть (`/v1/collectible-phones?...`). |
| GET | `/api/collectible-phones/{id}` | Параметры пробрасываются в Admin API (`/v1/collectible-phones/{id}?...`). |

## API: Premium

Все маршруты раздела требуют права `premium.manage` (проверяется и на уровне
панели, и на стороне Admin API).

| Метод | Путь | Описание |
| --- | --- | --- |
| GET | `/api/premium/plans` | Каталог Premium-планов (проксируется в Admin API `/v1/premium/plans`). |

## API: модерация

Все чтения пробрасывают query-параметры в Admin API как есть
(`/v1/moderation/...`). Решения — `POST` (см. ниже).

| Метод | Путь | Описание |
| --- | --- | --- |
| GET | `/api/moderation/cases` | Список кейсов модерации. |
| GET | `/api/moderation/cases/{id}` | Кейс модерации. |
| GET | `/api/moderation/reports/{id}` | Жалоба. |
| POST | `/api/moderation/cases/{id}/claim` | Взять кейс на себя. Тело: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/moderation/cases/{id}/decide` | Решение по кейсу. Тело: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/moderation/cases/{id}/appeals/{appeal_id}/review` | Рассмотрение апелляции. Тело: общие поля + `version` (int64), `internal_note` (string). |

## API: официальная верификация

Все маршруты требуют права `verification.review`. Чтения идут напрямую из
PostgreSQL, решения — всегда через Admin API (журнал команд, статусная машина,
оптимистичная блокировка).

| Метод | Путь | Параметры / тело |
| --- | --- | --- |
| GET | `/api/verification/applications` | Параметры: `status`, `target_type`, `reviewer`, `q`, `before_id`, `limit`. Ответ: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/verification/applications/{id}` | Ответ: заявка, события, `applicant_controls_target`, `target_verified`. |
| GET | `/api/verification/counts` | Счётчики заявок по статусам. |
| POST | `/api/verification/applications/{id}/claim` | Тело решения: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/verification/applications/{id}/approve` | Тело: общие поля + `version` (int64), `internal_note` (string). Выдаёт бейдж. |
| POST | `/api/verification/applications/{id}/reject` | Тело: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/actions/revoke-verification` | Снять бейдж. Требует `verification.review` **и** `verification.revoke`. Тело — см. ниже в разделе действий. |

Конфликт «решил другой модератор» возвращается как `409 Conflict`.

## API: сторонняя (ботовая) верификация

Отдельный механизм с отдельными таблицами, правами и маршрутами. Чтения и
решения очереди требуют `botverification.review`; управление верификаторами,
каталогом иконок и снятие меток — `botverification.manage`.

| Метод | Путь | Параметры / тело |
| --- | --- | --- |
| GET | `/api/botverification/verifiers` | Параметры: `enabled_only` (bool), `limit` (int). Ответ: `rows`. |
| GET | `/api/botverification/icons` | Параметры: `active_only` (bool), `limit` (int). Ответ: `rows`. |
| GET | `/api/botverification/marks` | Параметры: `peer_type`, `verifier_bot_id` (int64), `q`, `before_id` (int64), `limit` (int). Ответ: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/botverification/requests` | Параметры: `status`, `peer_type`, `verifier_bot_id` (int64), `q`, `before_id` (int64), `limit` (int). Ответ: `rows`, `has_more`, `next_before_id`. |
| GET | `/api/botverification/requests/{id}` | Ответ: `request`, `verifier`, `mark_active`. |
| GET | `/api/botverification/counts` | Счётчики запросов по статусам. Ответ: `counts`. |
| POST | `/api/botverification/requests/{id}/approve` | Тело решения: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/botverification/requests/{id}/reject` | Тело: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/botverification/requests/{id}/revoke` | Тело: общие поля + `version` (int64), `internal_note` (string). |
| POST | `/api/actions/grant-bot-verifier` | Назначить бота верификатором. Требует `botverification.manage`. Тело — см. ниже. |
| POST | `/api/actions/set-bot-verifier-enabled` | Включить/отключить верификатора. Требует `botverification.manage`. Тело — см. ниже. |
| POST | `/api/actions/revoke-bot-verifier` | Лишить бота статуса верификатора. Требует `botverification.manage`. Тело — см. ниже. |
| POST | `/api/actions/upsert-verification-icon` | Добавить/изменить иконку в каталоге. Требует `botverification.manage`. Тело — см. ниже. |
| POST | `/api/actions/set-verification-icon-active` | Включить/отключить иконку. Требует `botverification.manage`. Тело — см. ниже. |
| POST | `/api/actions/revoke-custom-verification` | Снять стороннюю метку. Требует `botverification.manage`. Тело — см. ниже. |

---

## API: действия над аккаунтами

Все маршруты — `POST /api/actions/...`, требуют сессию и CSRF. Тело запроса
всегда содержит **общие поля команд** (`command_id`, `reason`, `confirm`) плюс
поля из таблицы ниже.

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `set-frozen` | `user_id` (int64), `frozen` (bool), `freeze_until` (time, опц.), `freeze_appeal_url` (string, опц.) | — |
| `grant-premium` | `user_id` (int64), `months` (int) | `premium.manage` |
| `upsert-premium-plan` | `months` (int), `duration_days` (int), `amount_stars` (int64), `fiat_currency` (string), `fiat_amount` (int64), `store_product` (string), `store_quantity` (int), `enabled` (bool), `sort_order` (int), `label` (string), `expected_version` (int64) | `premium.manage` |
| `grant-stars` | `user_id` (int64), `amount` (int64) | — |
| `debit-stars` | `user_id` (int64), `amount` (int64, 1..1000000000); при недостаточном балансе списание отклоняется | — |
| `set-verified` | `user_id` (int64), `verified` (bool) | — |
| `set-account-flags` | `user_id` (int64), `scam` (bool), `fake` (bool) | — |
| `set-support` | `user_id` (int64), `support` (bool) | — |
| `set-account-username` | `user_id` (int64), `username` (string) | — |
| `set-account-profile` | `user_id` (int64), `first_name` (string), `last_name` (string) | — |
| `set-account-phone` | `user_id` (int64), `phone` (string) | — |
| `set-account-login-email` | `user_id` (int64), `email` (string) | — |
| `set-account-avatar` | **multipart**: поле `metadata` (JSON с общими полями + `user_id` (int64)) и файл в поле `file` | — |
| `set-account-color` | `user_id` (int64), `for_profile` (bool), `has_color` (bool), `color` (int), `background_emoji_id` (int64) | — |
| `set-account-emoji-status` | `user_id` (int64), `document_id` (int64), `until` (int, секунды) | — |
| `revoke-sessions` | `user_id` (int64), `hash` (int64, опц.), `keep_hash` (int64, опц.), `revoke_all` (bool) | — |

Пример (выдача Premium, как в вопросе выше):

```http
POST /api/actions/grant-premium
Content-Type: application/json
X-CSRF-Token: <csrf из /api/session>

{
  "command_id": "grant-premium-001",
  "reason": "Компенсация за инцидент",
  "confirm": true,
  "user_id": 123456789,
  "months": 12
}
```

## API: программный поиск аккаунта (v1)

`POST /v1/accounts/resolve-by-phone` — защищённый bearer-токеном, только
читающий поиск аккаунта, который использует Telegram-бот магазина в сценарии
«найти мой ID». Endpoint находит числовой ID аккаунта по номеру телефона без
приватной проекции просмотра, чтобы бот мог автоматически сохранить серверный
ID пользователя, если тот уже создал аккаунт. В отличие от всех маршрутов
`/api/actions/...` запрос не содержит метаданных команды: endpoint никогда не
меняет состояние и не пишет запись в аудит.

| Поле | Тип | Описание |
| --- | --- | --- |
| `phone` | string | Номер в любом формате (пробелы, дефисы, ведущий `+`); нормализуется на сервере функцией `domain.NormalizePhone`. |

Ответ `200 OK`:

| Поле | Тип | Описание |
| --- | --- | --- |
| `found` | bool | Найден ли аккаунт с этим номером. |
| `user_id` | int64 | Числовой ID аккаунта; `0`, если `found` равен `false`. |

Ошибки: `401` — неверный/отсутствующий bearer-токен; `500` — сбой поиска;
`501` — сборка без бэкенда поиска. Отсутствие номера — это успех с
`found: false`, а не `404`.

Пример:

```http
POST /v1/accounts/resolve-by-phone
Authorization: Bearer <token>
Content-Type: application/json

{ "phone": "+79991234567" }

// 200
{ "found": true, "user_id": 1780243207 }
```

## API: действия над каналами

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `set-channel-flags` | `channel_id` (int64), `scam` (bool), `fake` (bool) | — |
| `set-channel-settings` | `channel_id` (int64), `gigagroup` (*bool), `antispam` (*bool), `participants_hidden` (*bool), `noforwards` (*bool), `join_to_send` (*bool), `join_request` (*bool), `slowmode_seconds` (*int) — все опциональные указатели | — |
| `set-channel-username` | `channel_id` (int64), `username` (string) | — |
| `set-channel-color` | `channel_id` (int64), `for_profile` (bool), `has_color` (bool), `color` (int), `background_emoji_id` (int64) | — |
| `set-channel-emoji-status` | `channel_id` (int64), `document_id` (int64), `until` (int) | — |
| `set-channel-avatar` | **multipart**: `metadata` (JSON с общими полями + `channel_id` (int64)) и файл `file` | — |
| `set-channel-verified` | `channel_id` (int64), `verified` (bool) | — |

## API: действия над ботами

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `create-bot` | `owner_user_id` (int64), `name` (string), `username` (string) | — |
| `delete-bot` | `bot_user_id` (int64) | — |
| `export-bot-token` | `bot_user_id` (int64) | `bots.token.read` |
| `create-broadcast` | `message` (string), `target_mode` (string), `user_ids` ([]int64, опц.) | — |

## API: действия над стикерами и emoji

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `create-sticker-set` | **multipart**: `metadata` (JSON: общие поля + `title`, `short_name`, `kind`, `emoji`, `keywords`) и файл `file` | — |
| `rename-sticker-set` | `set_id` (int64), `title` (string) | — |
| `add-sticker-to-set` | **multipart**: `metadata` (JSON: общие поля + `set_id` (string), `emoji`, `keywords`) и файл `file` | — |
| `remove-sticker-from-set` | `set_id` (int64), `document_id` (int64) | — |
| `set-sticker-set-archived` | `set_id` (int64), `archived` (bool) | — |
| `set-sticker-set-sort-order` | `set_id` (int64), `sort_order` (int) | — |
| `delete-sticker-set` | `set_id` (int64) | — |

## API: действия над GIF-каталогом

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `create-gif-catalog-entry` | **multipart**: `metadata` (JSON: общие поля + `title`) и файл `file` | — |
| `set-gif-catalog-enabled` | `id` (int64, передаётся как строка), `enabled` (bool) | — |
| `set-gif-catalog-sort-order` | `id` (int64, строка), `sort_order` (int) | — |
| `delete-gif-catalog-entry` | `id` (int64, строка) | — |

## API: действия над подарками и коллекционками

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `import-gift` | **multipart**: `metadata` (JSON: общие поля + `gift_id` (int64), `title`, `stars` (int64), `convert_stars` (int64), `enabled` (bool), `sort_order` (int), `auction` (bool), `auction_slug`, `gifts_per_round` (int), `auction_start_date` (int), `auction_round_duration` (int), `availability_total` (int), `locked_until_date` (int)) и файл `file` | — |
| `import-official-gift` | общие поля + `source_gift_id` (string), `gift_id` (int64), `title`, `stars` (int64), `convert_stars` (int64), `enabled` (bool), `sort_order` (int), `include_collectible` (bool), `upgrade_stars` (int64), `supply_total` (int), `slug_prefix` (string), `locked_until_date` (int) | — |
| `publish-gift-collectibles` | **multipart**, `gift_id` передаётся в query (`?gift_id=`): `metadata` (JSON: общие поля + `upgrade_stars` (int64), `supply_total` (int), `slug_prefix` (string), `models` ([]объект), `patterns` ([]объект), `backdrops` ([]объект)); анимации — файлы по ключам из `models`/`patterns` | — |
| `set-gift-enabled` | `gift_id` (int64), `enabled` (bool) | — |
| `set-gift-sort-order` | `gift_id` (int64), `sort_order` (int) | — |
| `give-gift` | общие поля + `sender_user_id` (int64), `user_id` (int64), `channel_id` (int64), `gift_id` (int64), `hide_name` (bool), `message` (string), `upgrade` (bool), `model_attribute_id` (int64), `pattern_attribute_id` (int64), `backdrop_attribute_id` (int64) | — |
| `set-nft-gift-wallet` | общие поля + `ref` (string: slug, адрес NFT или числовой id), `wallet_name` (string, по умолчанию — введённое значение `wallet_address`), `wallet_address` (string: адрес TON mainnet; имя `.ton`, разрешаемое в цепи через TON DNS с запасным вариантом — flashfragment-имя пользователя с тем же именем, если в DNS нет записи; либо telegram-идентичность `rayo`, `@rayo`, `rayo.t.me`, `t.me/rayo`, разрешаемая только через записи flashfragment: claim имени, mint имени, подтверждённый кошелёк владельца коллекционного имени, TON Connect. Введённое имя никогда не сохраняется как адрес, а сработавшая запись попадает в details как `wallet_source`), `host_user_id` (int64, необязательно), `clear` (bool, вместо привязки кошелька возвращает подарок своему Telegram-владельцу) | — |

## API: действия над коллекционными юзернеймами

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `mint-collectible-username` | `username` (string), `owner_user_id` (int64), `owner_channel_id` (int64), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64), `url` (string), `purchase_date` (int/unix или дата) | — |
| `transfer-collectible-username` | `username` (string), `to_user_id` (int64), `to_channel_id` (int64) | — |
| `revoke-collectible-username` | `username` (string), `burn` (bool) | — |
| `delete-collectible-username` | `username` (string) | — |

## API: действия над анонимными номерами

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `mint-collectible-phone` | `phone` (string), `tier` (string), `owner_user_id` (int64), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64), `url` (string), `purchase_date` (int/unix или дата) | — |
| `update-collectible-phone-price` | `phone` (string), `currency` (string), `amount` (int64), `crypto_currency` (string), `crypto_amount` (int64) | — |
| `transfer-collectible-phone` | `phone` (string), `to_user_id` (int64) | — |
| `revoke-collectible-phone` | `phone` (string), `burn` (bool) | — |
| `delete-collectible-phone` | `phone` (string) | — |

## API: действия над рейтингом аккаунтов

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `recompute-account-rating` | `user_id` (int64) | — |
| `adjust-account-rating` | `user_id` (int64), `amount` (int64) | — |

## API: удаление сообщений

| Путь | Специфичные поля тела | Право |
| --- | --- | --- |
| `delete-messages` | `owner_user_id` (int64), `peer_id` (int64), `ids` ([]int), `revoke` (bool) | — |
| `delete-history` | `owner_user_id` (int64), `peer_id` (int64), `max_id` (int), `min_date` (int), `max_date` (int), `max_batches` (int), `just_clear` (bool), `revoke` (bool) | — |

## API: решения верификации (тела запросов)

Официальная верификация — `revoke-verification`:

| Поле | Тип | Описание |
| --- | --- | --- |
| `target_type` | string | Тип цели (`user` / `channel` и т.п., валидируется `domain.VerificationTargetType.Valid()`). |
| `target_id` | int64 | Идентификатор цели (не заявки!). |
| `internal_note` | string | Внутренняя заметка оператора (опц.). |
| + общие поля | | `command_id`, `reason`, `confirm`. |

Сторонняя верификация — действия верификатора/меток (`botverification.manage`):

| Путь | Специфичные поля тела |
| --- | --- |
| `grant-bot-verifier` | `bot_id` (int64), `icon_document_id` (int64), `company_name` (string, обяз.), `default_description` (string), `can_modify_custom_description` (bool), `version` (int64, 0 для нового) |
| `set-bot-verifier-enabled` | `bot_id` (int64), `enabled` (bool) |
| `revoke-bot-verifier` | `bot_id` (int64) |
| `upsert-verification-icon` | `document_id` (int64), `name` (string, обяз.), `owner_bot_id` (int64, опц., 0 = общая) |
| `set-verification-icon-active` | `icon_id` (int64), `active` (bool) |
| `revoke-custom-verification` | `verifier_bot_id` (int64), `peer_type` (string, валидируется), `peer_id` (int64) |

Все они также несут общие поля (`command_id`, `reason`, `confirm`).
