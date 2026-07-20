#!/usr/bin/env python3
"""python-telegram-bot echo demo for telesrv Bot API.

This is a normal python-telegram-bot program. The only telesrv-specific part is
the custom base_url/base_file_url pair.

Example:

    python cmd/bots/ptbecho/echo.py \
        --token "1780243224:..." \
        --base-url http://127.0.0.1:8081/bot \
        --base-file-url http://127.0.0.1:8081/file/bot

With BotFather privacy enabled, supergroup bots only receive commands, replies
to the bot, mentions, or messages otherwise visible to bots. In a group, send:

    /ping hello

For a command registered with ``is_ephemeral=true``, send:

    /private@YourBotUsername

The incoming Bot API message has ``message_id=0`` and carries the transient
identifier in ``api_kwargs`` until python-telegram-bot exposes the Bot API 10.2
fields directly. The demo replies through the same ephemeral action window.

The same program can also send proactive messages:

    python cmd/bots/ptbecho/echo.py \
        --token "1780243224:..." \
        --send-only \
        --send-chat-id -1000000000002 \
        --send-text "hello from python-telegram-bot"
"""

import argparse
import asyncio
import logging
import os
import signal
from typing import Iterable

from telegram import (
    Bot,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    KeyboardButton,
    ReplyKeyboardMarkup,
    Update,
)
from telegram.ext import (
    Application,
    ApplicationBuilder,
    CallbackQueryHandler,
    CommandHandler,
    ContextTypes,
    MessageHandler,
    filters,
)


LOG = logging.getLogger("ptbecho")


def env_int(name: str) -> int | None:
    raw = os.getenv(name)
    if raw is None or raw == "":
        return None
    try:
        return int(raw)
    except ValueError as exc:
        raise SystemExit(f"{name} must be an integer, got {raw!r}") from exc


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Echo bot demo using python-telegram-bot against telesrv.")
    parser.add_argument("--token", default=os.getenv("TELESRV_BOT_TOKEN"), help="Bot token, defaults to TELESRV_BOT_TOKEN")
    parser.add_argument("--base-url", default=os.getenv("TELESRV_BOT_API_BASE_URL", "http://127.0.0.1:8081/bot"))
    parser.add_argument("--base-file-url", default=os.getenv("TELESRV_BOT_API_BASE_FILE_URL", "http://127.0.0.1:8081/file/bot"))
    parser.add_argument("--prefix", default="echo: ")
    parser.add_argument("--ephemeral-prefix", default="ephemeral echo: ")
    parser.add_argument("--drop-pending", action="store_true", help="Drop pending updates before polling")
    parser.add_argument("--timeout", type=int, default=30, help="getUpdates long-poll timeout seconds")
    parser.add_argument(
        "--send-chat-id",
        type=int,
        default=env_int("TELESRV_BOT_DEMO_CHAT_ID"),
        help="Chat id for proactive sendMessage, defaults to TELESRV_BOT_DEMO_CHAT_ID",
    )
    parser.add_argument(
        "--send-text",
        default=os.getenv("TELESRV_BOT_DEMO_SEND_TEXT", ""),
        help="Text for proactive sendMessage, defaults to TELESRV_BOT_DEMO_SEND_TEXT",
    )
    parser.add_argument("--send-count", type=int, default=1, help="Number of proactive messages to send")
    parser.add_argument("--send-interval", type=float, default=1.0, help="Seconds between proactive sends")
    parser.add_argument(
        "--buttons-chat-id",
        type=int,
        default=env_int("TELESRV_BOT_DEMO_BUTTONS_CHAT_ID"),
        help="Send reply/inline keyboard validation messages to this chat on startup",
    )
    parser.add_argument("--send-only", action="store_true", help="Send proactive messages and exit without polling")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()
    if not args.token:
        parser.error("missing --token or TELESRV_BOT_TOKEN")
    if args.send_count < 1:
        parser.error("--send-count must be >= 1")
    if args.send_interval < 0:
        parser.error("--send-interval must be >= 0")
    if args.send_text and args.send_chat_id is None:
        parser.error("--send-chat-id is required when --send-text or --send-only is used")
    if args.send_only and not args.send_text and args.buttons_chat_id is None:
        parser.error("--send-only requires --send-text or --buttons-chat-id")
    return args


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if update.effective_message is None:
        return
    await update.effective_message.reply_text("send /ping <text> in a group, or any text in private chat")


async def ping(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    await echo(update, context)


def ephemeral_message_id(message: object) -> int | None:
    """Read a Bot API 10.2 field without depending on a PTB release cycle."""
    raw = getattr(message, "ephemeral_message_id", None)
    if raw is None:
        raw = (getattr(message, "api_kwargs", None) or {}).get("ephemeral_message_id")
    if isinstance(raw, int) and not isinstance(raw, bool) and raw > 0:
        return raw
    return None


async def send_echo(message: object, bot: Bot, prefix: str, ephemeral_prefix: str):
    text = getattr(message, "text", None) or getattr(message, "caption", None) or ""
    if not text:
        return None
    transient_id = ephemeral_message_id(message)
    if transient_id is None:
        return await message.reply_text(prefix + text)

    sender = getattr(message, "from_user", None)
    if sender is None:
        LOG.warning("ignored ephemeral message without from_user ephemeral_message_id=%s", transient_id)
        return None
    return await bot.send_message(
        chat_id=message.chat_id,
        text=ephemeral_prefix + text,
        api_kwargs={
            "receiver_user_id": sender.id,
            "reply_parameters": {"ephemeral_message_id": transient_id},
        },
    )


async def echo(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if update.effective_message is None or update.effective_chat is None:
        return
    text = update.effective_message.text or update.effective_message.caption or ""
    if not text:
        return
    prefix = context.application.bot_data.get("prefix", "echo: ")
    ephemeral_prefix = context.application.bot_data.get("ephemeral_prefix", "ephemeral echo: ")
    transient_id = ephemeral_message_id(update.effective_message)
    sent = await send_echo(update.effective_message, context.bot, prefix, ephemeral_prefix)
    if sent is None:
        return
    LOG.info(
        "echoed update_id=%s chat_id=%s message_id=%s ephemeral_message_id=%s "
        "sent_message_id=%s sent_ephemeral_message_id=%s text=%r",
        update.update_id,
        update.effective_chat.id,
        update.effective_message.message_id,
        transient_id,
        sent.message_id,
        ephemeral_message_id(sent),
        text,
    )


async def buttons(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if update.effective_chat is None:
        return
    await send_button_messages(context.bot, update.effective_chat.id)


async def callback(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    query = update.callback_query
    if query is None:
        return
    await query.answer("telesrv inline callback OK")
    LOG.info(
        "answered callback query_id=%s chat_id=%s message_id=%s data=%r",
        query.id,
        query.message.chat_id if query.message else None,
        query.message.message_id if query.message else None,
        query.data,
    )


async def send_active_messages(bot: Bot, chat_id: int, text: str, count: int, interval: float) -> None:
    for index in range(count):
        sent = await bot.send_message(chat_id=chat_id, text=text)
        LOG.info(
            "sent proactive index=%s/%s chat_id=%s message_id=%s text=%r",
            index + 1,
            count,
            chat_id,
            sent.message_id,
            text,
        )
        if index + 1 < count:
            await asyncio.sleep(interval)


async def send_button_messages(bot: Bot, chat_id: int) -> None:
    reply = await bot.send_message(
        chat_id=chat_id,
        text="TELESRV_REPLY_KEYBOARD_20260719",
        reply_markup=ReplyKeyboardMarkup(
            [[
                KeyboardButton("Primary", api_kwargs={"style": "primary"}),
                KeyboardButton("Success", api_kwargs={"style": "success"}),
                KeyboardButton("Danger", api_kwargs={"style": "danger"}),
            ]],
            resize_keyboard=True,
            one_time_keyboard=True,
            input_field_placeholder="Tap the reply button",
        ),
    )
    inline = await bot.send_message(
        chat_id=chat_id,
        text="TELESRV_INLINE_CALLBACK_20260719",
        reply_markup=InlineKeyboardMarkup(
            [[
                InlineKeyboardButton("Primary", callback_data="telesrv-primary", api_kwargs={"style": "primary"}),
                InlineKeyboardButton("Success", callback_data="telesrv-success", api_kwargs={"style": "success"}),
                InlineKeyboardButton("Danger", callback_data="telesrv-danger", api_kwargs={"style": "danger"}),
            ]],
        ),
    )
    LOG.info(
        "sent keyboard validation chat_id=%s reply_message_id=%s inline_message_id=%s",
        chat_id,
        reply.message_id,
        inline.message_id,
    )


async def send_on_startup(app: Application) -> None:
    chat_id = app.bot_data.get("send_chat_id")
    text = app.bot_data.get("send_text")
    if chat_id is not None and text:
        await send_active_messages(
            app.bot,
            chat_id=chat_id,
            text=text,
            count=int(app.bot_data.get("send_count", 1)),
            interval=float(app.bot_data.get("send_interval", 1.0)),
        )
    buttons_chat_id = app.bot_data.get("buttons_chat_id")
    if buttons_chat_id is not None:
        await send_button_messages(app.bot, int(buttons_chat_id))


async def post_init(app: Application) -> None:
    me = await app.bot.get_me()
    LOG.info("listening as @%s (%s), bot_api=%s", me.username or me.id, me.id, app.bot_data["base_url"])
    if (app.bot_data.get("send_chat_id") is not None and app.bot_data.get("send_text")) or app.bot_data.get(
        "buttons_chat_id"
    ) is not None:
        await send_on_startup(app)


def build_app(args: argparse.Namespace) -> Application:
    app = (
        ApplicationBuilder()
        .token(args.token)
        .base_url(args.base_url)
        .base_file_url(args.base_file_url)
        .post_init(post_init)
        .build()
    )
    app.bot_data["prefix"] = args.prefix
    app.bot_data["ephemeral_prefix"] = args.ephemeral_prefix
    app.bot_data["base_url"] = args.base_url
    app.bot_data["send_chat_id"] = args.send_chat_id
    app.bot_data["send_text"] = args.send_text
    app.bot_data["send_count"] = args.send_count
    app.bot_data["send_interval"] = args.send_interval
    app.bot_data["buttons_chat_id"] = args.buttons_chat_id
    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("ping", ping))
    app.add_handler(CommandHandler("private", echo))
    app.add_handler(CommandHandler("buttons", buttons))
    app.add_handler(CallbackQueryHandler(callback))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, echo))
    return app


async def run_send_only(args: argparse.Namespace) -> None:
    bot = Bot(token=args.token, base_url=args.base_url, base_file_url=args.base_file_url)
    me = await bot.get_me()
    LOG.info("authenticated as @%s (%s), bot_api=%s", me.username or me.id, me.id, args.base_url)
    if args.send_chat_id is not None and args.send_text:
        await send_active_messages(
            bot,
            chat_id=args.send_chat_id,
            text=args.send_text,
            count=args.send_count,
            interval=args.send_interval,
        )
    if args.buttons_chat_id is not None:
        await send_button_messages(bot, args.buttons_chat_id)


def stop_signals() -> Iterable[int] | None:
    if os.name == "nt":
        return None
    return (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)


def main() -> int:
    args = parse_args()
    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    logging.getLogger("httpx").setLevel(logging.WARNING)
    logging.getLogger("httpcore").setLevel(logging.WARNING)
    if args.send_only:
        asyncio.run(run_send_only(args))
        return 0

    app = build_app(args)
    app.run_polling(
        allowed_updates=["message", "edited_message", "callback_query"],
        drop_pending_updates=args.drop_pending,
        poll_interval=0.0,
        timeout=args.timeout,
        stop_signals=stop_signals(),
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
