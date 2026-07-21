import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import AsyncMock

from aiogram.enums import ParseMode
from aiogram.exceptions import TelegramBadRequest
from aiogram.methods import SendRichMessage
from aiogram.types import InputRichMessage


MODULE_PATH = Path(__file__).with_name("demo.py")
sys.path.insert(0, str(MODULE_PATH.parent))
SPEC = importlib.util.spec_from_file_location("bedolagaformat_demo", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
demo = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = demo
SPEC.loader.exec_module(demo)


class SentMessage:
    def __init__(self, message_id: int) -> None:
        self.message_id = message_id


class BedolagaFormatDemoTest(unittest.IsolatedAsyncioTestCase):
    def test_format_samples_cover_default_and_explicit_modes(self) -> None:
        samples = demo.format_samples("BEDOLAGA123")
        self.assertEqual(
            [sample.parse_mode for sample in samples],
            [None, ParseMode.MARKDOWN, ParseMode.MARKDOWN_V2],
        )
        self.assertIn("<b>BEDOLAGA123 Default HTML</b>", samples[0].text)
        self.assertIn("*BEDOLAGA123 Markdown*", samples[1].text)
        self.assertIn("*BEDOLAGA123 MarkdownV2*", samples[2].text)

    def test_markdown_v2_escapes_reserved_marker_characters(self) -> None:
        samples = demo.format_samples("BEDOLAGA-FULL-20260720")
        self.assertIn(r"BEDOLAGA\-FULL\-20260720", samples[2].text)

    async def test_send_suite_omits_parse_mode_only_for_default_html(self) -> None:
        bot = AsyncMock()
        bot.send_message.side_effect = [SentMessage(11), SentMessage(12), SentMessage(13)]

        message_ids = await demo.send_format_suite(bot, 1780243200, "BEDOLAGA123")

        self.assertEqual(message_ids, [11, 12, 13])
        calls = bot.send_message.await_args_list
        self.assertNotIn("parse_mode", calls[0].kwargs)
        self.assertEqual(calls[1].kwargs["parse_mode"], ParseMode.MARKDOWN)
        self.assertEqual(calls[2].kwargs["parse_mode"], ParseMode.MARKDOWN_V2)

    def test_rich_menu_covers_bedolaga_html_and_keyboard(self) -> None:
        html = demo.rich_menu_html("BEDOLAGA123", include_logo=False)
        self.assertIn("<h4>BEDOLAGA123 Admin</h4>", html)
        self.assertIn("<table bordered striped>", html)
        self.assertIn("<tg-time", html)
        self.assertIn("<details open>", html)
        self.assertIn("<footer>", html)
        markup = demo.rich_menu_keyboard()
        self.assertEqual(markup.inline_keyboard[0][0].callback_data, "menu:balance")
        self.assertEqual(markup.inline_keyboard[1][0].callback_data, "menu:info")

    async def test_rich_suite_retries_without_logo_and_edits(self) -> None:
        bot = AsyncMock()
        media_error = TelegramBadRequest(
            method=SendRichMessage(
                chat_id=1780243200,
                rich_message=InputRichMessage(html="<p>fixture</p>"),
            ),
            message="WEBPAGE_MEDIA_EMPTY",
        )
        bot.send_rich_message.side_effect = [
            media_error,
            SentMessage(21),
            SentMessage(22),
        ]
        bot.edit_message_text.return_value = SentMessage(21)

        ids = await demo.send_rich_suite(bot, 1780243200, "BEDOLAGA123")

        self.assertEqual(ids, [21, 22])
        sends = bot.send_rich_message.await_args_list
        self.assertEqual(len(sends), 3)
        self.assertIn("<img", sends[0].kwargs["rich_message"].html)
        self.assertNotIn("<img", sends[1].kwargs["rich_message"].html)
        self.assertIsNotNone(sends[2].kwargs["rich_message"].markdown)
        edit = bot.edit_message_text.await_args
        self.assertEqual(edit.kwargs["message_id"], 21)
        self.assertIn("EDITED", edit.kwargs["rich_message"].html)

    def test_login_demo_keyboard_has_login_url_and_plain_oidc_link(self) -> None:
        config = demo.LoginDemoConfig(
            issuer="https://oauth.example",
            client_id="9001",
            client_secret="secret",
            public_url="https://rp.example",
            listen_host="127.0.0.1",
            listen_port=3000,
        )
        markup = demo.login_demo_keyboard(config)
        login = markup.inline_keyboard[0][0].login_url
        self.assertIsNotNone(login)
        self.assertEqual(login.url, "https://rp.example/")
        self.assertTrue(login.request_write_access)
        self.assertEqual(markup.inline_keyboard[1][0].url, "https://rp.example/")

    async def test_send_login_demo_preserves_default_html_and_keyboard(self) -> None:
        config = demo.LoginDemoConfig(
            issuer="https://oauth.example",
            client_id="9001",
            client_secret="secret",
            public_url="https://rp.example",
            listen_host="127.0.0.1",
            listen_port=3000,
        )
        bot = AsyncMock()
        bot.send_message.return_value = SentMessage(31)

        message_id = await demo.send_login_demo(bot, 1780243200, "BEDOLAGA123", config)

        self.assertEqual(message_id, 31)
        call = bot.send_message.await_args
        self.assertNotIn("parse_mode", call.kwargs)
        self.assertEqual(
            call.kwargs["reply_markup"].inline_keyboard[0][0].login_url.url,
            "https://rp.example/",
        )


if __name__ == "__main__":
    unittest.main()
