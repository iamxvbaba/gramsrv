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
from aiogram.exceptions import TelegramBadRequest
from aiogram.filters import Command, CommandStart
from aiogram.types import (
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    InputRichMessage,
    LoginUrl,
    Message,
)

from login_demo import (
    LoginDemoConfig,
    LoginDemoServer,
    normalize_web_base,
    parse_listen,
)


LOG = logging.getLogger("bedolagaformat")
MARKER_RE = re.compile(r"^[A-Za-z0-9-]{1,64}$")
MARKDOWN_V2_RESERVED_RE = re.compile(r"([_\*\[\]\(\)~`>#+\-=|{}\.!\\])")


def env_flag(name: str) -> bool:
    return os.getenv(name, "").strip().lower() in {"1", "true", "yes", "on"}


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
    parser.add_argument(
        "--rich-menu",
        action="store_true",
        help="also send and edit Bedolaga-style rich HTML/Markdown menus",
    )
    parser.add_argument(
        "--rich-only",
        action="store_true",
        help="with --send-only, send only the rich menu suite",
    )
    parser.add_argument("--drop-pending", action="store_true")
    parser.add_argument("--polling-timeout", type=int, default=10)
    parser.add_argument("--marker", default=default_marker())
    parser.add_argument("--log-level", default="INFO")
    parser.add_argument(
        "--login-demo",
        action="store_true",
        default=env_flag("TELESRV_BOT_LOGIN_DEMO"),
        help="serve and send the Bedolaga Telegram Login/OIDC demo",
    )
    parser.add_argument(
        "--login-issuer",
        default=os.getenv("TELESRV_BOT_LOGIN_ISSUER", "http://127.0.0.1:2401"),
    )
    parser.add_argument(
        "--login-client-id",
        default=os.getenv("TELESRV_BOT_LOGIN_CLIENT_ID", ""),
    )
    parser.add_argument(
        "--login-client-secret",
        default=os.getenv("TELESRV_BOT_LOGIN_CLIENT_SECRET", ""),
        help="confidential OIDC secret; never printed (optional for JS-only demo)",
    )
    parser.add_argument(
        "--login-public-url",
        default=os.getenv("TELESRV_BOT_LOGIN_PUBLIC_URL", "http://127.0.0.1:3000"),
        help="registered origin where this demo is reachable",
    )
    parser.add_argument(
        "--login-listen",
        default=os.getenv("TELESRV_BOT_LOGIN_LISTEN", "127.0.0.1:3000"),
    )
    args = parser.parse_args()
    if not args.token:
        parser.error("missing --token or TELESRV_BOT_TOKEN")
    if args.send_only and args.send_chat_id is None:
        parser.error("--send-only requires --send-chat-id")
    if args.rich_only and (not args.send_only or args.send_chat_id is None):
        parser.error("--rich-only requires --send-only and --send-chat-id")
    if not MARKER_RE.fullmatch(args.marker):
        parser.error("--marker must contain 1-64 ASCII letters, digits, or hyphens")
    if not 0 <= args.polling_timeout <= 50:
        parser.error("--polling-timeout must be between 0 and 50")
    args.login_config = None
    if args.login_demo:
        if not re.fullmatch(r"[0-9]{1,64}", args.login_client_id):
            parser.error("--login-demo requires a numeric --login-client-id")
        try:
            issuer = normalize_web_base(args.login_issuer, name="login issuer")
            public_url = normalize_web_base(args.login_public_url, name="login public URL")
            listen_host, listen_port = parse_listen(args.login_listen)
        except ValueError as exc:
            parser.error(str(exc))
        args.login_config = LoginDemoConfig(
            issuer=issuer,
            client_id=args.login_client_id,
            client_secret=args.login_client_secret,
            public_url=public_url,
            listen_host=listen_host,
            listen_port=listen_port,
        )
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


def rich_menu_html(marker: str, *, include_logo: bool) -> str:
    """Build the rich HTML families used by Bedolaga's main menu."""
    logo = '<img src="https://example.com/bedolaga-logo.png">' if include_logo else ""
    return (
        f"{logo}<h4>{marker} Admin</h4>"
        "<h6>Subscription overview</h6><hr>"
        "<table bordered striped>"
        "<tr><th>Status</th><td align=\"right\">Active</td></tr>"
        "<tr><th>Updated</th><td align=\"right\">"
        '<tg-time unix="1700000000" format="R">now</tg-time>'
        "</td></tr></table>"
        "<details open><summary>Diagnostics</summary>"
        "<blockquote><code>rich menu online</code></blockquote></details>"
        "<footer>Choose an option</footer>"
    )


def rich_menu_markdown(marker: str) -> str:
    return (
        f"#### {marker} Markdown menu\n\n"
        "**Subscription:** Active\n\n"
        "> Rich Markdown transport is online.\n\n"
        "`callback keyboard preserved`"
    )


def rich_menu_keyboard() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Balance", callback_data="menu:balance"),
                InlineKeyboardButton(text="Buy", callback_data="menu:buy"),
            ],
            [InlineKeyboardButton(text="Info", callback_data="menu:info")],
        ]
    )


def login_demo_keyboard(config: LoginDemoConfig) -> InlineKeyboardMarkup:
    """Exercise both Telegram's login_url button and a plain OIDC page URL."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="Log in with Telegram",
                    login_url=LoginUrl(
                        url=config.public_url + "/",
                        forward_text="Bedolaga Login",
                        request_write_access=True,
                    ),
                )
            ],
            [InlineKeyboardButton(text="Open OIDC test page", url=config.public_url + "/")],
        ]
    )


async def send_login_demo(bot: Bot, chat_id: int, marker: str, config: LoginDemoConfig) -> int:
    message = await bot.send_message(
        chat_id=chat_id,
        text=(
            f"<b>{marker} Telegram Login</b>\n"
            "The first button validates Bot API <code>login_url</code>; "
            "the second page validates the local JS SDK and OIDC + PKCE."
        ),
        reply_markup=login_demo_keyboard(config),
    )
    LOG.info("sent Telegram Login demo chat_id=%s message_id=%s", chat_id, message.message_id)
    return message.message_id


def is_rich_media_retry_error(exc: TelegramBadRequest) -> bool:
    message = str(exc).lower()
    return "webpage_" in message or "media_empty" in message or "media_invalid" in message


async def send_rich_suite(bot: Bot, chat_id: int, marker: str) -> list[int]:
    """Exercise Bedolaga's send, no-logo retry, keyboard and rich edit path."""
    markup = rich_menu_keyboard()
    try:
        html_message = await bot.send_rich_message(
            chat_id=chat_id,
            rich_message=InputRichMessage(
                html=rich_menu_html(marker, include_logo=True),
                skip_entity_detection=True,
            ),
            reply_markup=markup,
        )
    except TelegramBadRequest as exc:
        if not is_rich_media_retry_error(exc):
            raise
        LOG.info("rich logo fetch rejected; retrying the menu without logo")
        html_message = await bot.send_rich_message(
            chat_id=chat_id,
            rich_message=InputRichMessage(
                html=rich_menu_html(marker, include_logo=False),
                skip_entity_detection=True,
            ),
            reply_markup=markup,
        )

    markdown_message = await bot.send_rich_message(
        chat_id=chat_id,
        rich_message=InputRichMessage(
            markdown=rich_menu_markdown(marker),
            skip_entity_detection=True,
        ),
        reply_markup=markup,
    )
    await bot.edit_message_text(
        chat_id=chat_id,
        message_id=html_message.message_id,
        rich_message=InputRichMessage(
            html=rich_menu_html(f"{marker} EDITED", include_logo=False),
            skip_entity_detection=True,
        ),
        reply_markup=markup,
    )
    ids = [html_message.message_id, markdown_message.message_id]
    LOG.info("sent rich menu suite chat_id=%s message_ids=%s", chat_id, ids)
    return ids


def build_dispatcher(marker: str, login_config: LoginDemoConfig | None = None) -> Dispatcher:
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

    @router.message(Command("richdemo"))
    async def rich_demo(message: Message) -> None:
        ids = await send_rich_suite(message.bot, message.chat.id, marker)
        LOG.info(
            "handled /richdemo chat_id=%s incoming_message_id=%s sent_message_ids=%s",
            message.chat.id,
            message.message_id,
            ids,
        )

    @router.message(Command("logindemo"))
    async def login_demo(message: Message) -> None:
        if login_config is None:
            await message.answer(
                "<b>Telegram Login demo is disabled.</b> Start this program with "
                "<code>--login-demo</code>."
            )
            return
        message_id = await send_login_demo(message.bot, message.chat.id, marker, login_config)
        LOG.info(
            "handled /logindemo chat_id=%s incoming_message_id=%s sent_message_id=%s",
            message.chat.id,
            message.message_id,
            message_id,
        )

    dispatcher = Dispatcher()
    dispatcher.include_router(router)
    return dispatcher


async def run(args: argparse.Namespace) -> None:
    bot = create_bot(args.token, args.base_url)
    login_server = LoginDemoServer(args.login_config, args.token) if args.login_config else None
    try:
        if login_server is not None:
            await login_server.start()
        me = await bot.get_me()
        LOG.info(
            "authenticated bot_id=%s username=@%s bot_api=%s marker=%s",
            me.id,
            me.username or "",
            args.base_url,
            args.marker,
        )
        if args.send_chat_id is not None:
            if not args.rich_only:
                await send_format_suite(bot, args.send_chat_id, args.marker)
            if args.rich_menu or args.rich_only:
                await send_rich_suite(bot, args.send_chat_id, args.marker)
            if args.login_config is not None:
                await send_login_demo(bot, args.send_chat_id, args.marker, args.login_config)
        if args.send_only:
            return

        await bot.delete_webhook(drop_pending_updates=args.drop_pending)
        dispatcher = build_dispatcher(args.marker, args.login_config)
        LOG.info(
            "polling started; send /start, /formatdemo, /richdemo or /logindemo to @%s",
            me.username or me.id,
        )
        await dispatcher.start_polling(
            bot,
            allowed_updates=["message"],
            polling_timeout=args.polling_timeout,
            close_bot_session=False,
        )
    finally:
        if login_server is not None:
            await login_server.close()
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
