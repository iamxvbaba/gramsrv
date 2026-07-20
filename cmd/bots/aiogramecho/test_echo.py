import unittest
from types import SimpleNamespace

import echo


class FakeBot:
    def __init__(self):
        self.calls = []

    async def send_message(self, **kwargs):
        self.calls.append(kwargs)
        return SimpleNamespace(message_id=0, ephemeral_message_id=88)


class FakeMessage:
    def __init__(self, *, text="hello", ephemeral_id=None):
        self.text = text
        self.caption = None
        self.chat = SimpleNamespace(id=-1000000000002)
        self.from_user = SimpleNamespace(id=1780243200)
        self.ephemeral_message_id = ephemeral_id
        self.model_extra = {}
        self.bot = FakeBot()
        self.answers = []

    async def answer(self, text):
        self.answers.append(text)
        return SimpleNamespace(message_id=1, ephemeral_message_id=None)


class EchoTest(unittest.IsolatedAsyncioTestCase):
    async def test_ephemeral_echo_uses_receiver_and_transient_reply(self):
        message = FakeMessage(text="/private@TetrisBot", ephemeral_id=77)

        await echo.send_echo(message, "aiogram echo: ", "ephemeral echo: ")

        self.assertEqual(message.answers, [])
        self.assertEqual(len(message.bot.calls), 1)
        call = message.bot.calls[0]
        self.assertEqual(call["chat_id"], -1000000000002)
        self.assertEqual(call["text"], "ephemeral echo: /private@TetrisBot")
        self.assertEqual(call["receiver_user_id"], 1780243200)
        self.assertEqual(call["reply_parameters"].ephemeral_message_id, 77)

    async def test_normal_echo_stays_on_standard_answer_path(self):
        message = FakeMessage()

        await echo.send_echo(message, "aiogram echo: ", "ephemeral echo: ")

        self.assertEqual(message.answers, ["aiogram echo: hello"])
        self.assertEqual(message.bot.calls, [])

    def test_extra_field_fallback_and_invalid_values(self):
        message = FakeMessage()
        message.model_extra = {"ephemeral_message_id": 66}
        self.assertEqual(echo.ephemeral_message_id(message), 66)
        message.model_extra = {"ephemeral_message_id": True}
        self.assertIsNone(echo.ephemeral_message_id(message))


if __name__ == "__main__":
    unittest.main()
