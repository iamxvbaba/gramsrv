import unittest
from types import SimpleNamespace

import echo


class FakeMessage:
    def __init__(self, *, text="hello", ephemeral_id=None):
        self.text = text
        self.caption = None
        self.chat_id = -1000000000002
        self.from_user = SimpleNamespace(id=1780243200)
        self.api_kwargs = {}
        if ephemeral_id is not None:
            self.api_kwargs["ephemeral_message_id"] = ephemeral_id
        self.replies = []

    async def reply_text(self, text):
        self.replies.append(text)
        return SimpleNamespace(message_id=1, api_kwargs={})


class FakeBot:
    def __init__(self):
        self.calls = []

    async def send_message(self, **kwargs):
        self.calls.append(kwargs)
        return SimpleNamespace(
            message_id=0,
            api_kwargs={"ephemeral_message_id": 88},
        )


class EchoTest(unittest.IsolatedAsyncioTestCase):
    async def test_ephemeral_echo_uses_receiver_and_transient_reply(self):
        message = FakeMessage(text="/private@TetrisBot", ephemeral_id=77)
        bot = FakeBot()

        await echo.send_echo(message, bot, "echo: ", "ephemeral echo: ")

        self.assertEqual(message.replies, [])
        self.assertEqual(
            bot.calls,
            [{
                "chat_id": -1000000000002,
                "text": "ephemeral echo: /private@TetrisBot",
                "api_kwargs": {
                    "receiver_user_id": 1780243200,
                    "reply_parameters": {"ephemeral_message_id": 77},
                },
            }],
        )

    async def test_normal_echo_stays_on_standard_reply_path(self):
        message = FakeMessage()
        bot = FakeBot()

        await echo.send_echo(message, bot, "echo: ", "ephemeral echo: ")

        self.assertEqual(message.replies, ["echo: hello"])
        self.assertEqual(bot.calls, [])

    def test_invalid_ephemeral_id_is_not_accepted(self):
        self.assertIsNone(echo.ephemeral_message_id(FakeMessage(ephemeral_id=0)))
        self.assertIsNone(echo.ephemeral_message_id(FakeMessage(ephemeral_id=True)))


if __name__ == "__main__":
    unittest.main()
