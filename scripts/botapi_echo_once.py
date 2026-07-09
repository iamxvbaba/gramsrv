import argparse
import asyncio
import sys
import time

from telegram import Bot


async def main() -> int:
    parser = argparse.ArgumentParser(description="Listen once via telesrv Bot API and echo the first text message.")
    parser.add_argument("--token", required=True)
    parser.add_argument("--base-url", default="http://127.0.0.1:8081/bot")
    parser.add_argument("--base-file-url", default="http://127.0.0.1:8081/file/bot")
    parser.add_argument("--timeout-seconds", type=int, default=180)
    parser.add_argument("--prefix", default="echo: ")
    args = parser.parse_args()

    bot = Bot(token=args.token, base_url=args.base_url, base_file_url=args.base_file_url)
    me = await bot.get_me()
    print(f"listening as @{me.username or me.id} ({me.id})", flush=True)
    await bot.delete_webhook(drop_pending_updates=False)

    offset = None
    stale = await bot.get_updates(timeout=0, allowed_updates=["message", "edited_message"])
    if stale:
        offset = max(update.update_id for update in stale) + 1
        await bot.get_updates(offset=offset, timeout=0, allowed_updates=["message", "edited_message"])
        print(f"drained {len(stale)} stale update(s), next offset={offset}", flush=True)

    deadline = time.monotonic() + max(1, args.timeout_seconds)
    while time.monotonic() < deadline:
        remaining = max(1, min(30, int(deadline - time.monotonic())))
        updates = await bot.get_updates(
            offset=offset,
            timeout=remaining,
            allowed_updates=["message", "edited_message"],
        )
        if not updates:
            continue
        offset = max(update.update_id for update in updates) + 1
        for update in updates:
            msg = update.message or update.edited_message
            if msg is None or msg.chat_id is None:
                continue
            text = msg.text or msg.caption or ""
            if not text:
                continue
            reply = args.prefix + text
            sent = await bot.send_message(chat_id=msg.chat_id, text=reply)
            print(
                f"echoed update_id={update.update_id} chat_id={msg.chat_id} "
                f"message_id={msg.message_id} sent_message_id={sent.message_id} text={text!r}",
                flush=True,
            )
            await bot.get_updates(offset=offset, timeout=0, allowed_updates=["message", "edited_message"])
            return 0

    print("timed out waiting for a text message", file=sys.stderr, flush=True)
    return 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
