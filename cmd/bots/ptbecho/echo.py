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

from telegram import Bot, Update
from telegram.constants import ChatAction
from telegram.ext import (
    Application,
    ApplicationBuilder,
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
    parser.add_argument("--send-only", action="store_true", help="Send proactive messages and exit without polling")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()
    if not args.token:
        parser.error("missing --token or TELESRV_BOT_TOKEN")
    if args.send_count < 1:
        parser.error("--send-count must be >= 1")
    if args.send_interval < 0:
        parser.error("--send-interval must be >= 0")
    wants_send = args.send_only or bool(args.send_text)
    if wants_send and args.send_chat_id is None:
        parser.error("--send-chat-id is required when --send-text or --send-only is used")
    if args.send_only and not args.send_text:
        parser.error("--send-only requires --send-text")
    return args


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if update.effective_message is None:
        return
    await update.effective_message.reply_text("send /ping <text> in a group, or any text in private chat")


async def ping(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    await echo(update, context)


async def echo(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    if update.effective_message is None or update.effective_chat is None:
        return
    text = update.effective_message.text or update.effective_message.caption or ""
    if not text:
        return
    prefix = context.application.bot_data.get("prefix", "echo: ")
    await context.bot.send_chat_action(chat_id=update.effective_chat.id, action=ChatAction.TYPING)
    sent = await update.effective_message.reply_text(prefix + text)
    LOG.info(
        "echoed update_id=%s chat_id=%s message_id=%s sent_message_id=%s text=%r",
        update.update_id,
        update.effective_chat.id,
        update.effective_message.message_id,
        sent.message_id,
        text,
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


async def send_on_startup(app: Application) -> None:
    chat_id = app.bot_data.get("send_chat_id")
    text = app.bot_data.get("send_text")
    if chat_id is None or not text:
        return
    await send_active_messages(
        app.bot,
        chat_id=chat_id,
        text=text,
        count=int(app.bot_data.get("send_count", 1)),
        interval=float(app.bot_data.get("send_interval", 1.0)),
    )


async def post_init(app: Application) -> None:
    me = await app.bot.get_me()
    LOG.info("listening as @%s (%s), bot_api=%s", me.username or me.id, me.id, app.bot_data["base_url"])
    if app.bot_data.get("send_chat_id") is not None and app.bot_data.get("send_text"):
        app.create_task(send_on_startup(app), name="ptbecho-proactive-send")


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
    app.bot_data["base_url"] = args.base_url
    app.bot_data["send_chat_id"] = args.send_chat_id
    app.bot_data["send_text"] = args.send_text
    app.bot_data["send_count"] = args.send_count
    app.bot_data["send_interval"] = args.send_interval
    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("ping", ping))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, echo))
    return app


async def run_send_only(args: argparse.Namespace) -> None:
    bot = Bot(token=args.token, base_url=args.base_url, base_file_url=args.base_file_url)
    me = await bot.get_me()
    LOG.info("authenticated as @%s (%s), bot_api=%s", me.username or me.id, me.id, args.base_url)
    await send_active_messages(
        bot,
        chat_id=args.send_chat_id,
        text=args.send_text,
        count=args.send_count,
        interval=args.send_interval,
    )


def stop_signals() -> Iterable[int]:
    if os.name == "nt":
        return (signal.SIGINT, signal.SIGTERM)
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
        allowed_updates=["message", "edited_message"],
        drop_pending_updates=args.drop_pending,
        poll_interval=0.0,
        timeout=args.timeout,
        stop_signals=stop_signals(),
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
