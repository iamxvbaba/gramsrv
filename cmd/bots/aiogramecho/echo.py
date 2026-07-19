#!/usr/bin/env python3
"""aiogram 3 echo/button demo for the telesrv Bot API endpoint."""

import argparse
import asyncio
import logging
import os

from aiogram import Bot, Dispatcher, F, Router
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.client.telegram import TelegramAPIServer
from aiogram.enums import ButtonStyle
from aiogram.filters import Command, CommandStart
from aiogram.types import (
    CallbackQuery,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    KeyboardButton,
    Message,
    ReplyKeyboardMarkup,
)
from aiogram.webhook.aiohttp_server import SimpleRequestHandler, setup_application
from aiohttp import web


LOG = logging.getLogger("aiogramecho")


def env_int(name: str) -> int | None:
    raw = os.getenv(name)
    if not raw:
        return None
    try:
        return int(raw)
    except ValueError as exc:
        raise SystemExit(f"{name} must be an integer, got {raw!r}") from exc


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="aiogram 3 echo bot against telesrv")
    parser.add_argument("--token", default=os.getenv("TELESRV_BOT_TOKEN"))
    parser.add_argument(
        "--base-url",
        default=os.getenv("TELESRV_BOT_API_SERVER", "http://127.0.0.1:8081"),
        help="API server origin; aiogram adds /bot<TOKEN> and /file/bot<TOKEN>",
    )
    parser.add_argument("--prefix", default="aiogram echo: ")
    parser.add_argument("--drop-pending", action="store_true")
    parser.add_argument("--mode", choices=("polling", "webhook"), default=os.getenv("TELESRV_BOT_MODE", "polling"))
    parser.add_argument(
        "--webhook-url",
        default=os.getenv("TELESRV_BOT_WEBHOOK_URL", ""),
        help="Public HTTPS URL including the webhook path",
    )
    parser.add_argument(
        "--webhook-path",
        default=os.getenv("TELESRV_BOT_WEBHOOK_PATH", "/webhook"),
        help="Local aiohttp route, normally the path part of --webhook-url",
    )
    parser.add_argument("--webhook-secret", default=os.getenv("TELESRV_BOT_WEBHOOK_SECRET", "telesrv-aiogram-demo"))
    parser.add_argument("--listen-host", default=os.getenv("TELESRV_BOT_LISTEN_HOST", "127.0.0.1"))
    parser.add_argument("--listen-port", type=int, default=env_int("TELESRV_BOT_LISTEN_PORT") or 8082)
    parser.add_argument("--delete-webhook-on-exit", action="store_true")
    parser.add_argument("--timeout", type=int, default=30)
    parser.add_argument("--send-chat-id", type=int, default=env_int("TELESRV_BOT_DEMO_CHAT_ID"))
    parser.add_argument("--send-text", default=os.getenv("TELESRV_BOT_DEMO_SEND_TEXT", ""))
    parser.add_argument("--buttons-chat-id", type=int, default=env_int("TELESRV_BOT_DEMO_BUTTONS_CHAT_ID"))
    parser.add_argument(
        "--button-icon-id",
        default=os.getenv("TELESRV_BOT_DEMO_BUTTON_ICON_ID"),
        help="Optional custom emoji document id used as the button icon",
    )
    parser.add_argument("--send-only", action="store_true")
    parser.add_argument("--log-level", default="INFO")
    args = parser.parse_args()
    if not args.token:
        parser.error("missing --token or TELESRV_BOT_TOKEN")
    if args.timeout < 0 or args.timeout > 50:
        parser.error("--timeout must be between 0 and 50")
    if args.send_text and args.send_chat_id is None:
        parser.error("--send-chat-id is required with --send-text")
    if args.send_only and not args.send_text and args.buttons_chat_id is None:
        parser.error("--send-only requires --send-text or --buttons-chat-id")
    if args.button_icon_id:
        try:
            if int(args.button_icon_id) <= 0:
                raise ValueError
        except ValueError as exc:
            raise SystemExit("--button-icon-id must be a positive integer") from exc
    if args.mode == "webhook" and not args.webhook_url:
        parser.error("--webhook-url or TELESRV_BOT_WEBHOOK_URL is required in webhook mode")
    if not args.webhook_path.startswith("/") or "?" in args.webhook_path or "#" in args.webhook_path:
        parser.error("--webhook-path must be an absolute path without query or fragment")
    if args.listen_port < 1 or args.listen_port > 65535:
        parser.error("--listen-port must be between 1 and 65535")
    return args


def reply_keyboard(icon_id: str | None) -> ReplyKeyboardMarkup:
    return ReplyKeyboardMarkup(
        keyboard=[
            [
                KeyboardButton(text="Primary", style=ButtonStyle.PRIMARY, icon_custom_emoji_id=icon_id),
                KeyboardButton(text="Success", style=ButtonStyle.SUCCESS, icon_custom_emoji_id=icon_id),
                KeyboardButton(text="Danger", style=ButtonStyle.DANGER, icon_custom_emoji_id=icon_id),
            ]
        ],
        resize_keyboard=True,
        one_time_keyboard=True,
        input_field_placeholder="Tap a colored reply button",
    )


def inline_keyboard(icon_id: str | None) -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="Primary",
                    callback_data="aiogram-primary",
                    style=ButtonStyle.PRIMARY,
                    icon_custom_emoji_id=icon_id,
                ),
                InlineKeyboardButton(
                    text="Success",
                    callback_data="aiogram-success",
                    style=ButtonStyle.SUCCESS,
                    icon_custom_emoji_id=icon_id,
                ),
                InlineKeyboardButton(
                    text="Danger",
                    callback_data="aiogram-danger",
                    style=ButtonStyle.DANGER,
                    icon_custom_emoji_id=icon_id,
                ),
            ]
        ]
    )


async def send_button_messages(bot: Bot, chat_id: int, icon_id: str | None) -> None:
    reply = await bot.send_message(
        chat_id=chat_id,
        text="TELESRV_AIOGRAM_REPLY_STYLES_20260719",
        reply_markup=reply_keyboard(icon_id),
    )
    inline = await bot.send_message(
        chat_id=chat_id,
        text="TELESRV_AIOGRAM_INLINE_STYLES_20260719",
        reply_markup=inline_keyboard(icon_id),
    )
    LOG.info(
        "sent styled buttons chat_id=%s reply_message_id=%s inline_message_id=%s",
        chat_id,
        reply.message_id,
        inline.message_id,
    )


def build_dispatcher(args: argparse.Namespace) -> Dispatcher:
    router = Router(name="telesrv-aiogramecho")

    @router.message(CommandStart())
    async def start(message: Message) -> None:
        await message.answer("send /ping <text>, /buttons, or any private text")

    @router.message(Command("buttons"))
    async def buttons(message: Message) -> None:
        await send_button_messages(message.bot, message.chat.id, args.button_icon_id)

    @router.message(Command("ping"))
    async def ping(message: Message) -> None:
        await message.answer(args.prefix + (message.text or ""))

    @router.callback_query(F.data.startswith("aiogram-"))
    async def callback(query: CallbackQuery) -> None:
        await query.answer(f"telesrv {query.data} callback OK")
        LOG.info("answered callback query_id=%s data=%r", query.id, query.data)

    @router.callback_query()
    async def fallback_callback(query: CallbackQuery) -> None:
        """Keep the echo demo responsive for buttons created by another demo."""
        await query.answer("telesrv callback OK")
        LOG.info("answered fallback callback query_id=%s data=%r", query.id, query.data)

    @router.message(F.text)
    async def echo(message: Message) -> None:
        await message.answer(args.prefix + (message.text or ""))
        LOG.info("echoed chat_id=%s message_id=%s", message.chat.id, message.message_id)

    dispatcher = Dispatcher()
    dispatcher.include_router(router)
    return dispatcher


def build_bot(args: argparse.Namespace) -> Bot:
    session = AiohttpSession(api=TelegramAPIServer.from_base(args.base_url.rstrip("/")))
    return Bot(token=args.token, session=session)


async def run(args: argparse.Namespace) -> None:
    bot = build_bot(args)
    runner: web.AppRunner | None = None
    try:
        me = await bot.get_me()
        LOG.info("authenticated as @%s (%s), bot_api=%s", me.username or me.id, me.id, args.base_url)
        if args.send_chat_id is not None and args.send_text:
            sent = await bot.send_message(chat_id=args.send_chat_id, text=args.send_text)
            LOG.info("sent proactive chat_id=%s message_id=%s", args.send_chat_id, sent.message_id)
        if args.buttons_chat_id is not None:
            await send_button_messages(bot, args.buttons_chat_id, args.button_icon_id)
        if args.send_only:
            return
        dispatcher = build_dispatcher(args)
        allowed_updates = ["message", "edited_message", "callback_query"]
        if args.mode == "polling":
            await bot.delete_webhook(drop_pending_updates=args.drop_pending)
            await dispatcher.start_polling(
                bot,
                allowed_updates=allowed_updates,
                polling_timeout=args.timeout,
                close_bot_session=False,
            )
            return

        application = web.Application()
        SimpleRequestHandler(
            dispatcher=dispatcher,
            bot=bot,
            secret_token=args.webhook_secret,
        ).register(application, path=args.webhook_path)
        setup_application(application, dispatcher, bot=bot)
        await bot.set_webhook(
            url=args.webhook_url,
            secret_token=args.webhook_secret,
            allowed_updates=allowed_updates,
            drop_pending_updates=args.drop_pending,
        )
        runner = web.AppRunner(application)
        await runner.setup()
        site = web.TCPSite(runner, host=args.listen_host, port=args.listen_port)
        await site.start()
        LOG.info(
            "webhook listening on http://%s:%s%s, public_url=%s",
            args.listen_host,
            args.listen_port,
            args.webhook_path,
            args.webhook_url,
        )
        await asyncio.Event().wait()
    finally:
        if args.mode == "webhook" and args.delete_webhook_on_exit:
            try:
                await bot.delete_webhook()
            except Exception:  # pragma: no cover - best-effort shutdown logging
                LOG.exception("failed to delete webhook during shutdown")
        if runner is not None:
            await runner.cleanup()
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
