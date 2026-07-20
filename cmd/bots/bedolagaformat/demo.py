#!/usr/bin/env python3
"""Bedolaga-style aiogram formatted-text demo for the telesrv Bot API.

The bot factory intentionally mirrors remnawave-bedolaga-telegram-bot:
DefaultBotProperties(parse_mode=ParseMode.HTML) is installed globally, while
individual sends may override it with legacy Markdown or MarkdownV2.
"""

from __future__ import annotations

import argparse
import asyncio
from dataclasses import dataclass
from datetime import datetime, timezone
import logging
import os
import re

from aiogram import Bot, Dispatcher, Router
from aiogram.client.default import DefaultBotProperties
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.client.telegram import TelegramAPIServer
from aiogram.enums import ParseMode
from aiogram.filters import Command, CommandStart
from aiogram.types import Message


LOG = logging.getLogger("bedolagaformat")
MARKER_RE = re.compile(r"^[A-Za-z0-9-]{1,64}$")
MARKDOWN_V2_RESERVED_RE = re.compile(r"([_\*\[\]\(\)~`>#+\-=|{}\.!\\])")


@dataclass(frozen=True)
class FormatSample:
    name: str
    text: str
    parse_mode: ParseMode | None


def default_marker() -> str:
    now = datetime.now(timezone.utc)
    return now.strftime("BEDOLAGA%Y%m%dT%H%M%SZ")


def escape_markdown_v2_text(value: str) -> str:
    return MARKDOWN_V2_RESERVED_RE.sub(r"\\\1", value)


def format_samples(marker: str) -> tuple[FormatSample, ...]:
    """Return deterministic messages whose labels are safe in every grammar."""
    markdown_v2_marker = escape_markdown_v2_text(marker)
    return (
        FormatSample(
            name="default_html",
            text=(
                f"<b>{marker} Default HTML</b> "
                "<i>italic 😀</i> <u>underline</u> "
                "<tg-spoiler>spoiler</tg-spoiler> "
                '<a href="https://example.com/bedolaga">link</a>'
            ),
            # Deliberately omitted from send_message: the Bedolaga factory default
            # must inject HTML just as it does for message.answer() in start.py.
            parse_mode=None,
        ),
        FormatSample(
            name="markdown",
            text=(
                f"*{marker} Markdown* _italic 😀_ "
                "[link](https://example.com/bedolaga) `code`"
            ),
            parse_mode=ParseMode.MARKDOWN,
        ),
        FormatSample(
            name="markdown_v2",
            text=(
                f"*{markdown_v2_marker} MarkdownV2* _italic 😀_ __underline__ "
                "~strike~ ||spoiler|| "
                "[link](https://example.com/bedolaga) `code`"
            ),
            parse_mode=ParseMode.MARKDOWN_V2,
        ),
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Bedolaga-style aiogram HTML/Markdown demo against telesrv"
    )
    parser.add_argument("--token", default=os.getenv("TELESRV_BOT_TOKEN"))
    parser.add_argument(
        "--base-url",
        default=os.getenv("TELESRV_BOT_API_SERVER", "http://127.0.0.1:8081"),
        help="API server origin; do not append /bot<TOKEN>",
    )
    parser.add_argument(
        "--send-chat-id",
        type=int,
        default=int(os.environ["TELESRV_BOT_DEMO_CHAT_ID"])
        if os.getenv("TELESRV_BOT_DEMO_CHAT_ID")
        else None,
        help="Send the complete suite proactively before polling",
    )
    parser.add_argument("--send-only", action="store_true")
    parser.add_argument("--drop-pending", action="store_true")
    parser.add_argument("--polling-timeout", type=int, default=10)
    parser.add_argument("--marker", default=default_marker())
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()
    if not args.token:
        parser.error("missing --token or TELESRV_BOT_TOKEN")
    if args.send_only and args.send_chat_id is None:
        parser.error("--send-only requires --send-chat-id")
    if not MARKER_RE.fullmatch(args.marker):
        parser.error("--marker must contain 1-64 ASCII letters, digits, or hyphens")
    if not 0 <= args.polling_timeout <= 50:
        parser.error("--polling-timeout must be between 0 and 50")
    return args


def create_bot(token: str, base_url: str) -> Bot:
    """Mirror Bedolaga's create_bot() with a custom Telegram API server."""
    session = AiohttpSession(
        api=TelegramAPIServer.from_base(base_url.rstrip("/"))
    )
    return Bot(
        token=token,
        session=session,
        default=DefaultBotProperties(parse_mode=ParseMode.HTML),
    )


async def send_format_suite(bot: Bot, chat_id: int, marker: str) -> list[int]:
    message_ids: list[int] = []
    for sample in format_samples(marker):
        if sample.parse_mode is None:
            sent = await bot.send_message(chat_id=chat_id, text=sample.text)
        else:
            sent = await bot.send_message(
                chat_id=chat_id,
                text=sample.text,
                parse_mode=sample.parse_mode,
            )
        message_ids.append(sent.message_id)
        LOG.info(
            "sent sample=%s chat_id=%s message_id=%s parse_mode=%s",
            sample.name,
            chat_id,
            sent.message_id,
            sample.parse_mode.value if sample.parse_mode is not None else "default-html",
        )
    return message_ids


def build_dispatcher(marker: str) -> Dispatcher:
    router = Router(name="telesrv-bedolaga-format")

    @router.message(CommandStart())
    async def start(message: Message) -> None:
        # No parse_mode argument: this is the exact failure shape from Bedolaga's
        # start handler when the Bot factory installs default HTML globally.
        await message.answer(
            f"<b>{marker} Start OK</b> <i>default HTML inherited</i> 😀"
        )
        LOG.info("handled /start chat_id=%s incoming_message_id=%s", message.chat.id, message.message_id)

    @router.message(Command("formatdemo"))
    async def format_demo(message: Message) -> None:
        ids = await send_format_suite(message.bot, message.chat.id, marker)
        LOG.info(
            "handled /formatdemo chat_id=%s incoming_message_id=%s sent_message_ids=%s",
            message.chat.id,
            message.message_id,
            ids,
        )

    dispatcher = Dispatcher()
    dispatcher.include_router(router)
    return dispatcher


async def run(args: argparse.Namespace) -> None:
    bot = create_bot(args.token, args.base_url)
    try:
        me = await bot.get_me()
        LOG.info(
            "authenticated bot_id=%s username=@%s bot_api=%s marker=%s",
            me.id,
            me.username or "",
            args.base_url,
            args.marker,
        )
        if args.send_chat_id is not None:
            await send_format_suite(bot, args.send_chat_id, args.marker)
        if args.send_only:
            return

        await bot.delete_webhook(drop_pending_updates=args.drop_pending)
        dispatcher = build_dispatcher(args.marker)
        LOG.info("polling started; send /start or /formatdemo to @%s", me.username or me.id)
        await dispatcher.start_polling(
            bot,
            allowed_updates=["message"],
            polling_timeout=args.polling_timeout,
            close_bot_session=False,
        )
    finally:
        await bot.session.close()


def main() -> int:
    args = parse_args()
    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    asyncio.run(run(args))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
