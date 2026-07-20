import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import AsyncMock

from aiogram.enums import ParseMode


MODULE_PATH = Path(__file__).with_name("demo.py")
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


if __name__ == "__main__":
    unittest.main()
