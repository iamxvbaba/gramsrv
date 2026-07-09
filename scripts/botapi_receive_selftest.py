import argparse
import asyncio
import sys

from telegram import Bot


async def main() -> int:
    parser = argparse.ArgumentParser(description="Poll telesrv Bot API with python-telegram-bot.")
    parser.add_argument("--token", required=True)
    parser.add_argument("--base-url", default="http://127.0.0.1:8081/bot")
    parser.add_argument("--base-file-url", default="http://127.0.0.1:8081/file/bot")
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument("--expect-chat-id", type=int, default=0)
    parser.add_argument("--expect-text", default="")
    parser.add_argument("--confirm", action="store_true")
    args = parser.parse_args()

    bot = Bot(token=args.token, base_url=args.base_url, base_file_url=args.base_file_url)
    me = await bot.get_me()
    print(f"getMe: id={me.id} username={me.username!r} is_bot={me.is_bot}")
    await bot.delete_webhook(drop_pending_updates=False)

    updates = await bot.get_updates(
        offset=args.offset or None,
        timeout=0,
        allowed_updates=["message", "edited_message"],
    )
    print(f"getUpdates: count={len(updates)}")
    for update in updates:
        msg = update.message or update.edited_message
        chat_id = msg.chat_id if msg else None
        text = msg.text if msg else None
        print(f"update_id={update.update_id} chat_id={chat_id} text={text!r}")

    if args.expect_text:
        matched = False
        for update in updates:
            msg = update.message or update.edited_message
            if msg is None:
                continue
            if args.expect_chat_id and msg.chat_id != args.expect_chat_id:
                continue
            if msg.text == args.expect_text:
                matched = True
                break
        if not matched:
            print("expected update not found", file=sys.stderr)
            return 1

    if args.confirm and updates:
        next_offset = max(update.update_id for update in updates) + 1
        after = await bot.get_updates(offset=next_offset, timeout=0, allowed_updates=["message", "edited_message"])
        print(f"confirm offset={next_offset}: count={len(after)}")
        if after:
            return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
