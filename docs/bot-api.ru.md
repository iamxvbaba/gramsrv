# HTTP Bot API: поддерживаемые методы

Документ перечисляет все методы Bot API, которые HTTP-шлюз telesrv
(`internal/botapi`, порт по умолчанию `8081`) действительно реализует, вместе
с принимаемыми параметрами, добавленными расширениями и официальными методами,
которые сознательно не поддержаны.

Английская версия этого документа: [bot-api.en.md](./bot-api.en.md).

## Как пользоваться этим документом

- [Транспорт](#транспорт)
- [Аутентификация](#аутентификация)
- [Формат запросов и ответов](#формат-запросов-и-ответов)
- [Ошибки](#ошибки)
- [Сводка методов](#сводка-методов)
- [Обновления](#обновления)
- [Методы: бот и команды](#методы-бот-и-команды)
- [Методы: отправка](#методы-отправка)
- [Методы: rich-сообщения](#методы-rich-сообщения)
- [Методы: ephemeral-сообщения](#методы-ephemeral-сообщения)
- [Методы: редактирование и удаление](#методы-редактирование-и-удаление)
- [Методы: файлы](#методы-файлы)
- [Методы: webhooks](#методы-webhooks)
- [Методы: меню, emoji status и web apps](#методы-меню-emoji-status-и-web-apps)
- [Методы: платежи](#методы-платежи)
- [Разметка и entities](#разметка-и-entities)
- [Лимиты](#лимиты)
- [Не реализовано](#не-реализовано)
- [Демо и self-тесты](#демо-и-self-тесты)

## Транспорт

| Параметр | Значение |
|---|---|
| Метод | `http(s)://<host>/bot<TOKEN>/<METHOD>` |
| Скачивание файлов | `http(s)://<host>/file/bot<TOKEN>/<file_path>` |
| Настройка слушателя | `TELESRV_BOT_API_ADDR`; пустое значение отключает шлюз |
| Лимит тела запроса | 25 MiB файла + 1 MiB служебных данных (`FILE_TOO_BIG`) |
| Content-Type | `application/json`, `multipart/form-data`, `application/x-www-form-urlencoded` |

`<METHOD>` сопоставляется без учёта регистра. Диспетчер доступен и по `GET`, и
по `POST`, поэтому `urlencoded`-запросы методом GET работают.

Настройка клиентов:

- `python-telegram-bot`: `--base-url http://host:8081/bot` и
  `--base-file-url http://host:8081/file/bot`;
- `aiogram`: `TelegramAPIServer.from_base("http://host:8081")`;
- grammY: `new Api("host:8081")` либо
  `api.config.use((ctx) => ({ apiRoot: "http://host:8081/bot" }))`.

## Аутентификация

Токен в пути должен быть парой `<bot_id>:<secret>` бота, а секрет — совпадать
со значением, сохранённым для этого бота. Всё остальное даёт HTTP `401` с
`ACCESS_TOKEN_INVALID`. Отдельного auth-заголовка нет: токен в URL и есть
credential, ровно как на официальном шлюзе.

Скачивание файлов (`/file/bot...`) аутентифицируется так же.

## Формат запросов и ответов

Успех:

```json
{"ok": true, "result": {}}
```

Ошибка:

```json
{"ok": false, "error_code": 400, "description": "CHAT_ID_INVALID"}
```

`error_code` — это HTTP-статус. `description` — маркер ошибки в верхнем регистре
(`SCREAMING_SNAKE`), не обязательно в официальных формулировках Telegram —
сопоставляйте по маркеру, а не по всей строке.

Разбор полей универсальный: JSON-тело «расплющивается» в строки, нестроковые
значения перемаршируются в свой JSON, а в `multipart` берётся первое значение
каждого поля. Поэтому часть методов принимает и сокращение `message_text`, и
полный объект `InputTextMessageContent`, а `setChatMenuButton` — и вложенный
объект `menu_button`, и «плоское» тело с `type` на верхнем уровне.

## Ошибки

Маркеры, которые шлюз может вернуть напрямую:

| Маркер | HTTP | Комментарий |
|---|---:|---|
| `ACCESS_TOKEN_INVALID` | 401 | неверный или неизвестный токен |
| `METHOD_NOT_FOUND` | 404 / 501 | неизвестный метод либо фича не собрана в этой сборке |
| `BAD_REQUEST` | 400 | неопознанная ошибка |
| `CHAT_ID_INVALID` | 400 | `chat_id` отсутствует, равен нулю или не разбирается |
| `MESSAGE_ID_INVALID` | 400 | `message_id` отсутствует или `<= 0` |
| `MESSAGE_IDENTIFIER_INVALID` | 400 | `inline_message_id` смешан с `chat_id`/`message_id` |
| `MESSAGE_EMPTY` | 400 | не передан `text` |
| `MESSAGE_TOO_LONG` | 400 | превышен лимит текста |
| `MESSAGE_NOT_MODIFIED` | 400 | правка ничего не меняет |
| `REPLY_PARAMETERS_INVALID` | 400 | конфликтующие селекторы ответа |
| `REPLY_MESSAGE_ID_INVALID` | 400 | отрицательный или некорректный адресат ответа |
| `MESSAGE_THREAD_ID_INVALID` | 400 | вне диапазона id верхнего сообщения ephemeral-треда |
| `ENTITY_INVALID`, `ENTITY_BOUNDS_INVALID`, `ENTITY_TYPE_UNSUPPORTED`, `ENTITIES_TOO_LONG` | 400 | проблемы со списком entity |
| `FILE_ID_INVALID`, `FILE_TOO_BIG`, `MEDIA_INVALID` | 400 | проблемы с файлом/медиа |
| `BUTTON_INVALID`, `BUTTON_TYPE_INVALID`, `BUTTON_DATA_INVALID`, `BUTTON_URL_INVALID` | 400 | проблемы с разметкой |
| `RICH_MESSAGE_INVALID`, `RICH_MESSAGE_TOO_LONG`, `RICH_MESSAGE_DATE_INVALID`, `RICH_MESSAGE_BLOCKS_UNSUPPORTED`, `RICH_MESSAGE_MEDIA_UNSUPPORTED`, `RICH_MESSAGE_OPTION_UNSUPPORTED` | 400 | проблемы rich-сообщения |
| `WEBPAGE_MEDIA_EMPTY` | 400 | rich HTML сослался на удалённое медиа, которое локальный blob-backend не может материализовать |
| `EFFECT_ID_INVALID`, `VALUE_INVALID` | 400 | некорректный числовой параметр |
| `BOT_COMMAND_INVALID` | 400 | некорректный или слишком большой `commands` |
| `BOT_COMMAND_SCOPE_UNSUPPORTED` | 400 | не-default scope или задан `language_code` |
| `EPHEMERAL_MESSAGE_ID_INVALID`, `EPHEMERAL_TARGET_REQUIRED`, `EPHEMERAL_ACTION_EXPIRED` | 400 | проблемы адресации ephemeral-сообщений |
| `QUERY_ID_INVALID`, `USER_ID_INVALID`, `RESULT_ID_INVALID`, `RESULT_ID_EMPTY`, `RESULT_TYPE_INVALID` | 400 | проблемы query/user/result |
| `OFFSET_INVALID` | 400 | offset в `getUpdates` меньше `-10000` |
| `ALLOWED_UPDATES_INVALID` | 400 | не JSON-массив из не более 100 строк |
| `ALLOWED_UPDATES_UNSUPPORTED` | 501 | шлюз без сервиса управления обновлениями |
| `MAX_CONNECTIONS_INVALID` | 400 | вне `1..100` |
| `SECRET_TOKEN_INVALID` | 400 | символы вне `[A-Za-z0-9_-]` или длиннее 256 |
| `WEBHOOK_URL_INVALID` | 400 | не абсолютный `http(s)`-URL, длиннее 2048, с userinfo/fragment или с портом вне `1..65535` |
| `CERTIFICATE_PINNING_UNSUPPORTED` | 400 | передан `certificate` или загруженный сертификат |
| `IP_ADDRESS_UNSUPPORTED` | 400 | передан `ip_address` |
| `WEBHOOK_UNSUPPORTED` | 501 | шлюз без сервиса webhooks |
| `METHOD_NOT_FOUND` | 501 | `answerShippingQuery` |
| `BLOCKED_USER_EMOJI_STATUS_SERVICE_MISSING`, `BLOCKED_WEBAPP_QUERY_SERVICE_MISSING`, `BLOCKED_PREPARED_INLINE_SERVICE_MISSING` | 501 | опциональный сервис не собран |
| `USER_PERMISSION_DENIED` | 403 | emoji status без нужного разрешения пользователя |
| `PREMIUM_ACCOUNT_REQUIRED` | 400 | emoji status без Premium |
| `PREMIUM_GIFT_SELF_INVALID`, `PREMIUM_GIFT_CODE_INVALID`, `BALANCE_TOO_LOW`, `STAR_COUNT_INVALID`, `MONTH_COUNT_INVALID` | 400 | проблемы подарка Premium |
| `IDEMPOTENCY_KEY_INVALID` | 400 | конфликт `request_id`/`Idempotency-Key` либо плохой charset/длина |
| `PAYMENT_FORM_INVALID` | 400 | платёжная форма отклонена |
| `CHAT_WRITE_FORBIDDEN`, `CHAT_ADMIN_REQUIRED` | 400 | не хватает прав |
| `USER_BOT_REQUIRED` | 400 | адресат не является ботом |
| `CONFLICT: ...` | 409 | параллельный опрос или активная доставка webhook |
| `INTERNAL_SERVER_ERROR` | 500 / 503 | сбой хранилища или транспорта |

## Сводка методов

Реализовано (37 методов + 1 файловый маршрут):

| Группа | Методы |
|---|---|
| Бот и команды | `getMe`, `setMyCommands`, `deleteMyCommands`, `getMyCommands` |
| Обновления | `getUpdates` |
| Текст | `sendMessage`, `sendRichMessage` |
| Медиа | `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendLivePhoto`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice` |
| Гео | `sendContact`, `sendLocation`, `sendVenue` |
| Правка/удаление | `editMessageText`, `deleteMessage` |
| Правка/удаление ephemeral | `editEphemeralMessageText`, `editEphemeralMessageMedia`, `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage` |
| Callback | `answerCallbackQuery` |
| Файлы | `getFile`, `GET /file/bot<TOKEN>/<file_path>` |
| Webhooks | `setWebhook`, `deleteWebhook`, `getWebhookInfo` |
| Меню и статус | `setChatMenuButton`, `getChatMenuButton`, `setUserEmojiStatus` |
| Web apps и inline | `answerWebAppQuery`, `savePreparedInlineMessage` |
| Платежи | `giftPremiumSubscription` |
| Заблокировано по design | `answerShippingQuery` (HTTP 501) |

## Обновления

Шлюз порождает только три вида обновлений:

| Значение `allowed_updates` | Ключ обновления |
|---|---|
| `message` | `message` |
| `edited_message` | `edited_message` |
| `callback_query` | `callback_query` |

`allowed_updates` — JSON-массив из не более 100 строк; неизвестные имена
разбираются без ошибки, но не совпадают ни с чем, поэтому не рассчитывайте на
фильтрацию как на способ спрятать обновления — отбрасывайте лишние поля в
боте. Настройка принимается и `getUpdates`, и `setWebhook` и хранится отдельно
для каждого бота.

Ephemeral-сообщения (Bot API 10.2) приходят как обычные `message`-обновления,
но их `message_id` принадлежит ephemeral-пространству, а не нумерации
сообщений чата, поэтому обычный `reply_to_message_id` их не адресует. Ответ
идёт через `reply_parameters.ephemeral_message_id`; см.
[Методы: ephemeral-сообщения](#методы-ephemeral-сообщения).

Время жизни очереди задаёт `TELESRV_BOT_API_UPDATE_RETENTION` (по умолчанию
`24h`); подтверждённые обновления удаляются раньше.

## Методы: бот и команды

### `getMe`

Без параметров. Возвращает собственный объект `User` бота.

### `setMyCommands`

| Параметр | Тип | Комментарий |
|---|---|---|
| `commands` | JSON-массив | до 100 записей вида `{command, description, is_ephemeral}` |
| `scope` | объект | принимается только `{"type":"default"}` |
| `language_code` | строка | должно быть пустым |

`is_ephemeral=true` помечает команду как ephemeral: её вызов в группе создаёт
сообщение, видимое только вызвавшему, и бот должен ответить в пределах
ephemeral-окна. Возвращает `true`.

### `deleteMyCommands`

Те же правила по `scope`, что и у `setMyCommands`; очищает список. Возвращает
`true`.

### `getMyCommands`

Те же правила по `scope`. Возвращает сохранённый массив; поле
`is_ephemeral: true` присутствует только у помеченных команд.

## Методы: отправка

### `sendMessage`

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен, не ноль; отрицательный для групп/каналов |
| `text` | строка | обязателен, до 4096 символов после разбора разметки |
| `parse_mode` | строка | `HTML`, `Markdown`, `MarkdownV2` или пусто для обычного текста |
| `entities` | JSON-массив | используется при отсутствии `parse_mode`, иначе игнорируется |
| `reply_markup` | объект | любой из четырёх конструкторов, см. [Разметка](#разметка-и-entities) |
| `disable_web_page_preview` | булево | |
| `disable_notification` | булево | |
| `reply_to_message_id` | целое | |
| `receiver_user_id` | целое | **расширение telesrv**: переключает вызов в ephemeral-отправку |
| `callback_query_id` | целое | ephemeral-отправка, привязанная к callback query |
| `reply_parameters` | объект | `{ephemeral_message_id}` для ephemeral-ответа, `{message_id}` для обычного |
| `message_thread_id` | целое | ephemeral: id верхнего сообщения в треде |

Обычная и ephemeral-адресация взаимоисключающи: если задан
`receiver_user_id`, поле `reply_to_message_id` должно остаться пустым, а
`reply_parameters` может нести только `ephemeral_message_id`. Для
ephemeral-отправки допустимы лишь inline-клавиатуры (иначе
`BUTTON_TYPE_INVALID`). Результат — та же структура `Message`, где
`message_id` содержит ephemeral-id.

### `sendPhoto`, `sendAnimation`, `sendAudio`, `sendDocument`, `sendSticker`, `sendVideo`, `sendVideoNote`, `sendVoice`

У них один общий обработчик.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен |
| `<kind>` | строка / файл | одноимённое поле метода: `photo`, `animation`, `audio`, `document`, `sticker`, `video`, `video_note`, `voice` |
| `caption` | строка | до 1024 символов |
| `parse_mode`, `caption_entities` | | как в `sendMessage` |
| `reply_markup` | объект | все четыре конструктора |
| `disable_notification`, `reply_to_message_id` | | |
| `width`, `height`, `duration`, `title`, `performer` | целое / строка | принимаются и передаются как подсказки метаданных |
| `emoji` | строка | только для стикеров |
| `receiver_user_id`, `callback_query_id`, `reply_parameters`, `message_thread_id` | | то же ephemeral-расширение, что и в `sendMessage` |

Файловое поле принимает, по убыванию приоритета:

1. `attach://<field>`, ссылающееся на multipart-часть;
2. загруженную multipart-часть с тем же именем поля;
3. URL `http://` или `https://` (сервер забирает его сам);
4. `file_id`, ранее выданный `getFile`.

Всё остальное — `FILE_ID_INVALID`.

### `sendLivePhoto`

Два медиа-поля: `photo` (кадр) и `live_photo` (видеочасть). Оба принимают
загрузку, `attach://` или `file_id`. Видеочасть **не может** быть HTTP-URL —
это `FILE_ID_INVALID`, как и в официальном ограничении. Подписи, `parse_mode` и
ephemeral-расширение работают так же, как в остальных медиа-методах.

### `sendContact`, `sendLocation`, `sendVenue`

Эти три метода **только ephemeral**: они требуют `receiver_user_id` (или
`callback_query_id`), иначе `EPHEMERAL_TARGET_REQUIRED`.

`sendContact` принимает `chat_id`, `phone_number`, `first_name`, необязательные
`last_name` и `vcard` (до 2048 байт), плюс необязательную inline-разметку.

`sendLocation` принимает `chat_id`, `latitude` (`-90..90`), `longitude`
(`-180..180`), `horizontal_accuracy` (`0..1500`, по умолчанию `0`). Ненулевой
`live_period` отклоняется с `MEDIA_INVALID` — live-локация не реализована.

`sendVenue` добавляет обязательные `title` и `address` и принимает либо
`foursquare_id` + `foursquare_type`, либо `google_place_id` +
`google_place_type`.

## Методы: rich-сообщения

### `sendRichMessage`

Расширение telesrv, rich-текст Layer 228. Отправляет `InputRichMessage` вместо
обычной отформатированной строки, поэтому таблицы, заголовки, разделители,
блоки `details` и `footer` доходят до клиента настоящими rich-блоками.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` | целое | обязателен |
| `rich_message` | объект | `{"html": "..."}` **или** `{"markdown": "..."}`, плюс необязательные `is_rtl` и `skip_entity_detection` |
| `reply_markup` | объект | только inline-клавиатуры |
| `reply_parameters` | объект | `{"message_id": N}`; устаревшее `reply_to_message_id` тоже принимается, но не оба сразу |
| `disable_notification`, `protect_content` | булево | |
| `message_effect_id` | целое | id эффекта сообщения |

Правила и отказы:

- ровно одно из `html` / `markdown` должно быть непустым; ноль или оба —
  `RICH_MESSAGE_INVALID`;
- `blocks` разбирается, но отклоняется — `RICH_MESSAGE_BLOCKS_UNSUPPORTED`;
- `media` разбирается, но отклоняется — `RICH_MESSAGE_MEDIA_UNSUPPORTED`;
- исходник ограничен 256 KiB;
- `business_connection_id`, `message_thread_id`,
  `direct_messages_topic_id`, `allow_paid_broadcast` и
  `suggested_post_parameters` отклоняются явной ошибкой, а не игнорируются
  молча;
- rich HTML со ссылкой на удалённое медиа (`img`, `video`, `audio`, `tg-map`,
  `tg-collage`, `tg-slideshow`) падает с `WEBPAGE_MEDIA_EMPTY`, потому что
  локальный blob-backend не может атомарно материализовать произвольный URL.
  Описанный в `cmd/bots/bedolagaformat` обходной путь — повторить запрос один
  раз без логотипа, не деградируя до classic-меню.

Поддерживаемые HTML-элементы: `b`/`strong`, `i`/`em`, `u`/`ins`,
`s`/`strike`/`del`, `tg-spoiler`, `span` (только как
`<span class="tg-spoiler">`), `a`, `code`, `pre` (с `class="language-…"`),
`blockquote`, `tg-emoji`, `tg-time`, `footer` и `table` (с атрибутами
`bordered` и `striped`, выравниванием по ячейкам). Для `tg-time` обязателен
`unix` в диапазоне `1..2^31-1` и `format`, собранный из `t`, `T`, `d`, `D`,
`w`/`W` либо ровно `r`/`R` для относительной отметки времени.

Markdown разбирает тот же rich-builder, что и на стороне MTProto, поэтому
работают заголовки `#`, разделители `---`, pipe-таблицы, цитаты `>`, огороженный
код и footer-блоки.

`editMessageText` принимает тот же объект `rich_message`; отдельного метода
`editMessageRichMessage` нет.

## Методы: ephemeral-сообщения

Ephemeral-сообщения (Bot API 10.2) видны только получателю, который видит
пометку «видно только вам». telesrv поддерживает весь жизненный цикл.

Отправка идёт через обычные методы с `receiver_user_id`:

- `sendMessage` — текст;
- медиа-методы — медиа плюс подпись;
- `sendContact`, `sendLocation`, `sendVenue` — только ephemeral.

Правила адресации:

- `receiver_user_id` обязателен и должен быть положительным целым;
- вместо него якорем может быть `callback_query_id` (получатель берётся из
  query);
- `reply_parameters.ephemeral_message_id` отвечает на существующее
  ephemeral-сообщение и не комбинируется с `callback_query_id`;
- `reply_parameters.message_id` — обычная форма ответа, с получателем не
  комбинируется;
- `message_thread_id` — верхнее сообщение ephemeral-треда;
- окно действия — 15 секунд, после него сервер отвечает
  `EPHEMERAL_ACTION_EXPIRED`.

Правка и удаление:

| Метод | Параметры |
|---|---|
| `editEphemeralMessageText` | `chat_id`, `receiver_user_id`, `ephemeral_message_id`, `text`, `parse_mode`, `entities`, `reply_markup` |
| `editEphemeralMessageCaption` | то же, с `caption` + `caption_entities` |
| `editEphemeralMessageMedia` | то же, плюс `media` (см. ниже) |
| `editEphemeralMessageReplyMarkup` | то же, только `reply_markup` |
| `deleteEphemeralMessage` | `chat_id`, `receiver_user_id`, `ephemeral_message_id` |

`media` — объект в духе `InputMedia`: `type` (одно из `photo`, `animation`,
`audio`, `document`, `video`, `live_photo`), файловое поле и необязательные
`caption`, `parse_mode`, `caption_entities`, `width`, `height`, `duration`,
`title`, `performer`. Для `live_photo` кадр берётся из `photo`, видео — из
`media`. Сырая загрузка здесь не допускается — только `file_id`, ссылка на файл
без `attach://` или HTTPS-URL (иначе `FILE_ID_INVALID`).

`reply_markup` принимается только inline, и важно его *наличие*: переданный
пустой `reply_markup` снимает клавиатуру.

## Методы: редактирование и удаление

### `editMessageText`

Единственный метод правки. Покрывает и обычный текст, и rich-текст, и
сообщения чата, и inline-сообщения.

| Параметр | Тип | Комментарий |
|---|---|---|
| `chat_id` + `message_id` | | один способ адресации |
| `inline_message_id` | строка | другой способ; не смешивать с первым |
| `text` | строка | одна форма содержимого |
| `rich_message` | объект | другая форма содержимого; не смешивать с `text` |
| `parse_mode`, `entities` | | как в `sendMessage` |
| `reply_markup` | объект | только inline; наличие с пустым значением снимает |
| `disable_web_page_preview` | булево | |

Для `inline_message_id` результат — булево значение, для сообщения чата —
обновлённый `Message`. Правка, ничего не меняющая, даёт
`MESSAGE_NOT_MODIFIED`.

Методов `editMessageCaption`, `editMessageMedia`, `editMessageReplyMarkup`,
`editMessageLiveLocation` и `stopMessageLiveLocation` нет — используйте
`editMessageText` (или ephemeral-варианты).

### `deleteMessage`

`chat_id` + `message_id`. Возвращает `true`. Удаление уже удалённого или
чужого сообщения возвращает `false` либо ошибку, как и на официальном шлюзе.

### `answerCallbackQuery`

| Параметр | Тип | Комментарий |
|---|---|---|
| `callback_query_id` | строка | обязателен |
| `text` | строка | текст уведомления |
| `url` | строка | |
| `show_alert` | булево | модальное окно вместо тоста |
| `cache_time` | целое | секунды |

Возвращает `true`.

## Методы: файлы

### `getFile`

`file_id` должен быть выдан telesrv. В результате всегда
`file_unique_id == file_id` и `file_path == file_id`, поэтому URL скачивания —
`/file/bot<TOKEN>/<file_id>`. Чужие или неизвестные id — `FILE_ID_INVALID`.

### `GET /file/bot<TOKEN>/<file_path>`

Отдаёт файл потоком, чанк за чанком, с сохранённым MIME-типом и
`Content-Length`. Путь — это `file_id`; путь с лишними сегментами даёт
`404 FILE_NOT_FOUND`.

## Методы: webhooks

### `setWebhook`

| Параметр | Тип | Комментарий |
|---|---|---|
| `url` | строка | пустое значение, наоборот, удаляет webhook |
| `secret_token` | строка | `[A-Za-z0-9_-]`, до 256 символов |
| `max_connections` | целое | `1..100`, по умолчанию `40` |
| `allowed_updates` | JSON-массив | см. [Обновления](#обновления) |
| `drop_pending_updates` | булево | |

`certificate` (и любая загруженная часть сертификата) отклоняется с
`CERTIFICATE_PINNING_UNSUPPORTED` — нужен публично доверенный сертификат.
`ip_address` отклоняется с `IP_ADDRESS_UNSUPPORTED`: шлюз использует
сконфигурированный список адресов.

Установка webhook во время long-polling `getUpdates` или во время доставки
возвращает `409 CONFLICT`.

Поведение доставки:

- один HTTP `POST` на обновление, тело — **объект `Update`**, а не массив, как
  у `getUpdates`;
- заголовок `X-Telegram-Bot-Api-Secret-Token` несёт секретный токен;
- `Content-Type: application/json`; успехом считается любой `2xx`;
- редиректы не следуются, поэтому секрет не утечёт на другой хост;
- параллельно доставляется до `max_connections` обновлений, суммарно 16 ботов
  и 64 одновременных HTTP-запроса;
- при сбое повтор экспоненциально от 1s до 5 минут плюс небольшой
  бот-зависимый джиттер;
- доставка подтверждается до первого неуспешного обновления, так что ничего
  не пропускается.

### `getWebhookInfo`

Возвращает `url`, `has_custom_certificate` (здесь всегда `false`),
`pending_update_count` и — если webhook настроен — `max_connections`,
`allowed_updates`, `last_error_date`, `last_error_message`.

### `deleteWebhook`

`drop_pending_updates` необязателен. Во время активной доставки возвращает
`409`.

## Методы: меню, emoji status и web apps

### `setChatMenuButton` / `getChatMenuButton`

`menu_button` — это `{"type": "default"}`, `{"type": "commands"}` или
`{"type": "web_app", "text": "...", "web_app": {"url": "..."}}`. Также
принимается «плоское» тело с `type` на верхнем уровне — это держит
работоспособность старых сериализаторов библиотек. Установка возвращает `true`,
чтение — текущую кнопку (никогда не `null`: незаданная кнопка читается как
`{"type": "default"}`).

### `setUserEmojiStatus`

`user_id`, `emoji_status_custom_emoji_id`, `emoji_status_expiration_date`.
Требуется, чтобы пользователь разрешил боту менять свой статус (иначе
`403 USER_PERMISSION_DENIED`) и чтобы он был Premium
(`PREMIUM_ACCOUNT_REQUIRED`). Возвращает `true`.

### `answerWebAppQuery`

`web_app_query_id` плюс `result`. Поддерживается только `InlineQueryResult` типа
`article`: `id` (до 64 символов), `title`, `description`, `url` (только HTTPS),
`input_message_content` (или сокращение `message_text`), `reply_markup` (только
inline). Другие типы результатов — `RESULT_TYPE_INVALID`. В ответе есть
`inline_message_id`, если ответ его породил.

### `savePreparedInlineMessage`

`user_id`, `result` (то же ограничение на `article`) и фильтры пиров
`allow_user_chats`, `allow_bot_chats`, `allow_group_chats`,
`allow_channel_chats`. Возвращает `{id, expiration_date}`.

## Методы: платежи

### `giftPremiumSubscription`

Оплачивает подарок Premium из Stars-баланса бота через тот же надёжный
Stars-пайплайн, что и MTProto `payments.sendStarsForm`.

| Параметр | Тип | Комментарий |
|---|---|---|
| `user_id` | целое | получатель; подарок себе отклоняется |
| `month_count` | целое | `1..120` |
| `star_count` | целое | положительное, не более 10^15 |
| `text` | строка | до 128 символов |
| `text_parse_mode`, `text_entities` | | ограниченный набор entity |
| `request_id` | строка | расширение telesrv для идемпотентности |

Передавайте HTTP-заголовок `Idempotency-Key` (или локальное поле `request_id`) —
допустимый charset `[A-Za-z0-9]` плюс `-`, `_`, `.`, `:`, до 128 символов.
Повтор того же ключа с тем же запросом возвращает сохранённый результат; повтор
с другим получателем, тарифом, ценой или текстом — `IDEMPOTENCY_KEY_INVALID`.
Запросы без ключа тоже работают, но каждый ретрай списывает средства снова,
поэтому ключ всегда стоит слать.

### `dropPendingUpdates`

Работает: отбрасывает неподтверждённые апдейты в очереди бота и отвечает `true`.
Нужен боту, который отстал, — иначе он годами продирается через бэклог, а
`pre_checkout_query` живёт всего 10 секунд и протухает незамеченным.

### `answerPreCheckoutQuery`

Работает. Перед списанием Stars плательщик ждёт ответа бота **не более 10 секунд**
(требование Telegram). Бот получает апдейт `pre_checkout_query` и отвечает
`pre_checkout_query_id` плюс `ok` и, при `ok=false`, `error_message`.

- `ok=true` — Stars списываются, боту приходит `successful_payment`;
- `ok=false` — оплата отменяется, `error_message` показывается плательщику;
- нет ответа за 10 секунд — оплата считается несостоявшейся (`PAYMENT_FAILED`).

Таймаут не переходит в «списать напрямую»: не ответивший бот означает отказ, иначе
pre-checkout ничего бы не проверял. Ответ принимается только от бота, которому
принадлежит заказ; на устаревший `pre_checkout_query_id` бот получает
`QUERY_ID_INVALID`. Сумма и `invoice_payload` в запросе не сверяются с тем, что бот
записывал при `sendInvoice`, — проверка остаётся на стороне бота, как и в Telegram.

### `answerShippingQuery`

Метод распознаётся, но отвечает HTTP `501 METHOD_NOT_FOUND`: оплата Stars не имеет
шага доставки, отвечать не на что.
## Разметка и entities

`reply_markup` принимает ровно один конструктор:

| Конструктор | Комментарий |
|---|---|
| `inline_keyboard` | ряды кнопок |
| `keyboard` | reply-клавиатура; `resize_keyboard`, `one_time_keyboard`, `is_persistent`, `input_field_placeholder`, `selective` |
| `remove_keyboard` | `remove_keyboard: true`, `selective` |
| `force_reply` | `force_reply: true`, `input_field_placeholder`, `selective` |

Кнопки inline-клавиатуры (ровно одно действие на кнопку, иначе
`BUTTON_INVALID`): `url`, `callback_data` (до 64 байт), `web_app`, `login_url`
(`url`, `bot_username`, `forward_text`, `request_write_access`),
`switch_inline_query`, `switch_inline_query_current_chat`,
`switch_inline_query_chosen_chat`, `copy_text`. У каждой кнопки также есть
`text` (обязателен), `style` (`primary` / `danger` / `success` / пусто) и
`icon_custom_emoji_id`. Неизвестные поля отклоняются, а не игнорируются.

Кнопки reply-клавиатуры: обычная строка либо объект с `text` и одним из
`request_contact`, `request_location`, `request_poll` (`type`), `request_users`
(`request_id`, `user_is_bot`, `user_is_premium`, `max_quantity`,
`request_name`, `request_username`, `request_photo`), `request_chat`
(`request_id`, `chat_is_channel`, `chat_is_forum`, `chat_has_username`,
`chat_is_created`, `user_administrator_rights`, `bot_administrator_rights`,
`bot_is_member`, `request_title`, `request_username`, `request_photo`) или
`web_app`.

Message entities (для `entities` / `caption_entities`, а также entity
`date_time`, которая принимает `unix_time` + `date_time_format`):

`bold`, `italic`, `underline`, `strikethrough`, `code`, `pre` (`language`),
`text_link` (`url`), `text_mention` (`user.id`), `spoiler`, `blockquote`,
`expandable_blockquote`, `custom_emoji` (`custom_emoji_id`), `mention`,
`hashtag`, `cashtag`, `bot_command`, `url`, `email`, `phone_number`,
`bank_card_number`, `date_time`.

Лимиты: 256 entity на сообщение (`ENTITIES_TOO_LONG`), смещения в кодовых
единицах UTF-16, неизвестные типы — `ENTITY_TYPE_UNSUPPORTED`.

## Лимиты

| Лимит | Значение |
|---|---|
| Длина текста | 4096 символов после разбора |
| Длина подписи | 1024 символа |
| Entity на сообщение | 256 |
| Id inline-результата | 64 байта |
| Callback data | 64 байта |
| Команд в `setMyCommands` | 100 |
| Размер загрузки | 25 MiB на файл |
| Записей в `allowed_updates` | 100 |
| `limit` в `getUpdates` | по умолчанию и максимум 100 |
| `timeout` в `getUpdates` | ограничивается диапазоном 0..50 секунд |
| `max_connections` | 1..100, по умолчанию 40 |
| Исходник rich-сообщения | 256 KiB |
| `offset`/`limit` в `getFile` | `offset` игнорируется, размер отдаётся целиком |

## Не реализовано

Все остальные официальные методы возвращают HTTP `404 METHOD_NOT_FOUND`. Чаще
всего до них доходят:

- правка: `editMessageCaption`, `editMessageMedia`,
  `editMessageReplyMarkup`, `editMessageLiveLocation`,
  `stopMessageLiveLocation`, `stopPoll`;
- медиагруппы и остальной `send*`-набор: `sendMediaGroup`, `sendPoll`,
  `sendDice`, `sendGame`, `sendChatAction`;
- платежи: `createInvoiceLink`, `sendInvoice`, `answerPreCheckoutQuery`,
  `answerShippingQuery`, `getStarTransactions`, `refundStarPayment`;
- администрирование чатов: `banChatMember`, `unbanChatMember`,
  `restrictChatMember`, `promoteChatMember`, `setChatPermissions`,
  `setChatPhoto`, `deleteChatPhoto`, `pinChatMessage`, `unpinChatMessage`,
  `unpinAllChatMessages`, `setChatTitle`, `setChatDescription`, `leaveChat`;
- запросы: `getChat`, `getChatAdministrators`, `getChatMember`,
  `getChatMemberCount`, `getForumTopic*`, `getUserChatBoosts`;
- сообщения: `forwardMessage`, `forwardMessages`, `copyMessage`,
  `copyMessages`, `sendPoll` и смежные с `getFile` помощники;
- администрирование бота: `setMyName`, `setMyDescription`,
  `setMyShortDescription`, `setMyDefaultAdministratorRights`, `close`,
  `logOut`;
- реакции, игры и методы business-аккаунтов.

MTProto-клиенты это не затрагивает: всё, что реализовано для официальных
клиентов Android/iOS/Desktop, доступно по MTProto, а не по HTTP. Общие
Stars- и Premium-пайплайны описаны в `docs/premium-stars.md`.

## Демо и self-тесты

| Путь | Назначение |
|---|---|
| `cmd/bots/ptbecho` | echo на `python-telegram-bot` против шлюза, включая демо ephemeral-команды |
| `cmd/bots/aiogramecho` | echo на aiogram 3, ephemeral-ответы, reply- и inline-клавиатуры |
| `cmd/bots/bedolagaformat` | HTML / Markdown / MarkdownV2, rich-меню через `sendRichMessage`, Telegram Login |
| `cmd/bots/botcheck` | Go-проба: импортирует токен бота по MTProto и проверяет цикл бота |
| `cmd/bots/botdemo` | Go-пример: бот шлёт в канал или пользователю по таймеру |
| `cmd/bots/grammystore` | Продовый grammY-бот на HTTP-шлюзе (Postgres, собственный деплой) |
| `scripts/botapi_echo_once.py` | Одноразовый self-тест отправки/приёма |
| `scripts/botapi_receive_selftest.py` | Self-тест очереди обновлений и webhook |
