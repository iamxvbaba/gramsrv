#!/usr/bin/env python3
"""Snapshot the official Telegram star-gift catalog for gramsrv.

Replicates the output of cmd/giftfetch (snapshot manifest schema v2) using a
user Telegram account through Telethon. The produced directory is a drop-in
replacement for the `TELESRV_OFFICIAL_GIFTS_DIR` snapshot root read by
internal/officialgifts: manifest.json plus the referenced document resources
under `documents/`.

Every downloaded animation is checked against the same rules the server applies
in internal/app/stargifts.PrepareOfficialAnimation. A document that the admin
panel would reject makes the run abort *before* manifest.json is rewritten, so a
bad download can never become a snapshot that breaks gift import.

Login is interactive: the phone prompt, the login-code prompt and the 2FA
password prompt appear on the console. The Telethon session file is reused on
later runs, so an already-logged-in account never prompts again.

Only regular gifts are exported; unique/crafted rows in the catalog response are
skipped because the server snapshot loader rejects them.

Usage:
    python3 giftfetch_telethon.py --session gift-importer \
        --out /opt/flashgram/shared/data/official-gifts
    python3 giftfetch_telethon.py --phone +79990001122 --out /path/to/snapshot
    python3 giftfetch_telethon.py --self-test --out /tmp/giftfetch-selftest

The script is idempotent: documents already on disk with a matching size and
SHA-256 are reused, so repeated runs only download newly referenced files.
"""

import argparse
import asyncio
import getpass
import gzip
import hashlib
import json
import os
import re
import sys
import tempfile
import zlib

from telethon import TelegramClient, errors, sessions
from telethon.tl import functions, types
from telethon.tl.types.payments import StarGifts as PaymentsStarGifts

API_ID = 17349
API_HASH = "344583e45741c457fe1862106095a5eb"
MANIFEST_SCHEMA = 2
MAX_CATALOG_GIFTS = 5000
MAX_DOC_BYTES = 16 << 20
DEFAULT_WORKERS = 8

# Mirrors domain.MaxStarGiftTGSBytes / MaxStarGiftLottieBytes /
# MaxStarGiftAnimationFrameRate / MaxStarGiftAnimationSeconds and the 512x512
# frame check in internal/app/stargifts.animation.go.
MAX_TGS_BYTES = 512 << 10
MAX_LOTTIE_BYTES = 4 << 20
MAX_ANIMATION_FPS = 120.0
MAX_ANIMATION_SECONDS = 30.0
LOTTIE_WIDTH = 512
LOTTIE_HEIGHT = 512

ANIMATION_EXTENSIONS = {".tgs", ".json", ".lottie"}
KNOWN_EXTENSIONS = {".tgs", ".json", ".lottie", ".webm", ".mp4", ".webp", ".png", ".jpg", ".jpeg"}
CRAFTED_RARITY_KINDS = ("uncommon", "rare", "epic", "legendary")


def document_extension(file_name, mime_type):
    ext = os.path.splitext(file_name or "")[1].lower()
    if ext in KNOWN_EXTENSIONS:
        return ext
    mime = (mime_type or "").lower()
    if mime in ("application/x-tgsticker", "application/gzip"):
        return ".tgs"
    if mime == "application/json":
        return ".json"
    if mime == "video/webm":
        return ".webm"
    if mime == "video/mp4":
        return ".mp4"
    if mime == "image/webp":
        return ".webp"
    if mime == "image/png":
        return ".png"
    if mime == "image/jpeg":
        return ".jpg"
    return ".bin"


def document_names(document):
    file_name = ""
    alt = ""
    for attribute in document.attributes:
        if isinstance(attribute, types.DocumentAttributeFilename):
            file_name = os.path.basename(attribute.file_name or "")
        elif isinstance(attribute, types.DocumentAttributeSticker):
            alt = attribute.alt or ""
        elif isinstance(attribute, types.DocumentAttributeCustomEmoji):
            alt = attribute.alt or ""
    return file_name, alt


def rarity_manifest(rarity):
    if isinstance(rarity, types.StarGiftAttributeRarity):
        return {"kind": "permille", "permille": int(rarity.permille)}
    for kind, cls in (
        ("uncommon", types.StarGiftAttributeRarityUncommon),
        ("rare", types.StarGiftAttributeRarityRare),
        ("epic", types.StarGiftAttributeRarityEpic),
        ("legendary", types.StarGiftAttributeRarityLegendary),
    ):
        if isinstance(rarity, cls):
            return {"kind": kind}
    raise ValueError("unsupported rarity constructor: %r" % (rarity,))


def unix_or_zero(value):
    if not value:
        return 0
    if hasattr(value, "timestamp"):
        return int(value.timestamp())
    return int(value)


def background_manifest(background):
    if background is None:
        return None
    return {
        "center_color": int(background.center_color),
        "edge_color": int(background.edge_color),
        "text_color": int(background.text_color),
    }


def is_gzip(data):
    return len(data) >= 2 and data[0] == 0x1F and data[1] == 0x8B


def decompress_single_tgs(data):
    """Single gzip member, no trailing bytes, bounded output - as Go does."""
    if not data or len(data) > MAX_TGS_BYTES:
        return None
    member = zlib.decompressobj(31)
    try:
        raw = member.decompress(data, MAX_LOTTIE_BYTES + 1)
        if member.unconsumed_tail:
            return None
        raw += member.flush()
    except zlib.error:
        return None
    if not member.eof or member.unused_data:
        return None
    if len(raw) > MAX_LOTTIE_BYTES:
        return None
    return raw


def as_number(value):
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    return float(value)


def validate_lottie(raw):
    payload = bytes(raw).strip()
    if payload.startswith(b"\xef\xbb\xbf"):
        payload = payload[3:]
    if not payload or len(payload) > MAX_LOTTIE_BYTES:
        return False, "empty or oversized lottie payload"
    try:
        root = json.loads(payload)
    except ValueError:
        return False, "not a JSON lottie document"
    if not isinstance(root, dict):
        return False, "lottie root is not an object"
    if not root.get("v"):
        return False, "lottie has no version"
    width = as_number(root.get("w"))
    height = as_number(root.get("h"))
    if width != LOTTIE_WIDTH or height != LOTTIE_HEIGHT:
        return False, "lottie is %sx%s, expected %dx%d" % (
            root.get("w"), root.get("h"), LOTTIE_WIDTH, LOTTIE_HEIGHT)
    frame_rate = as_number(root.get("fr"))
    if frame_rate is None or not 0 < frame_rate <= MAX_ANIMATION_FPS:
        return False, "frame rate %r outside (0, %g]" % (root.get("fr"), MAX_ANIMATION_FPS)
    in_point = as_number(root.get("ip"))
    out_point = as_number(root.get("op"))
    if in_point is None or in_point < 0:
        return False, "in point %r is negative or not a number" % (root.get("ip"),)
    if out_point is None or out_point <= in_point:
        return False, "out point %r does not exceed in point" % (root.get("op"),)
    if out_point - in_point > frame_rate * MAX_ANIMATION_SECONDS:
        return False, "animation spans more than %g s" % MAX_ANIMATION_SECONDS
    if not root.get("layers"):
        return False, "lottie has no layers"
    for asset in root.get("assets") or []:
        if not isinstance(asset, dict):
            return False, "lottie asset is not an object"
        for key in ("p", "u"):
            value = asset.get(key)
            if value is not None and value != "":
                return False, "external assets are not allowed (%s=%r)" % (key, value)
    try:
        compact = json.dumps(root, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    except (TypeError, ValueError):
        return False, "lottie is not serializable"
    if len(gzip.compress(compact)) > MAX_TGS_BYTES:
        return False, "recompressed lottie exceeds %d bytes" % MAX_TGS_BYTES
    return True, ""


def validate_animation(file_name, data):
    """Mirror of stargifts.prepareAnimationWithPolicy for official documents."""
    name = os.path.basename((file_name or "").strip())
    ext = os.path.splitext(name)[1].lower()
    if ext == ".tgs" or is_gzip(data):
        raw = decompress_single_tgs(data)
        if raw is None:
            return False, "not a single gzip-compressed lottie member"
    else:
        if ext not in ANIMATION_EXTENSIONS:
            return False, "expected .tgs, .json or plain .lottie"
        if not data or len(data) > MAX_LOTTIE_BYTES:
            return False, "lottie payload %d outside (0, %d]" % (len(data), MAX_LOTTIE_BYTES)
        raw = data
    return validate_lottie(raw)


class DocumentSink:
    """Collects documents and writes them under <out>/documents/<id><ext>."""

    def __init__(self, out_dir, max_bytes, force):
        self.out_dir = out_dir
        self.max_bytes = max_bytes
        self.force = force
        self.documents = {}

    def register(self, document, purpose):
        if not isinstance(document, types.Document) or not document.id:
            raise ValueError("%s references an invalid document" % purpose)
        if document.size <= 0 or document.size > self.max_bytes:
            raise ValueError(
                "%s document %d size %d outside (0, %d]"
                % (purpose, document.id, document.size, self.max_bytes)
            )
        existing = self.documents.get(document.id)
        if existing is None:
            existing = {
                "document": document,
                "purposes": [purpose],
            }
            self.documents[document.id] = existing
        elif existing["document"].size != document.size or existing["document"].mime_type != document.mime_type:
            raise ValueError("document %d has conflicting metadata" % document.id)
        elif purpose not in existing["purposes"]:
            existing["purposes"].append(purpose)
        return document

    def relative_path(self, document):
        file_name, _ = document_names(document)
        ext = document_extension(file_name, document.mime_type)
        return os.path.join("documents", "%d%s" % (document.id, ext))

    def existing_bytes(self, relative):
        full = os.path.join(self.out_dir, relative)
        try:
            with open(full, "rb") as handle:
                data = handle.read()
        except FileNotFoundError:
            return None
        if len(data) <= 0 or len(data) > self.max_bytes:
            return None
        return data

    def unique_path(self, document, data):
        relative = self.relative_path(document)
        full = os.path.join(self.out_dir, relative)
        if self.force or not os.path.exists(full) or os.path.getsize(full) == len(data):
            return relative
        base, ext = os.path.splitext(relative)
        return "%s.%d%s" % (base, len(data), ext)

    async def download(self, client, worker_limiter, source):
        document = source["document"]
        relative = self.relative_path(document)
        file_name, _ = document_names(document)
        if not file_name:
            file_name = os.path.basename(relative)
        if not self.force:
            present = self.existing_bytes(relative)
            if present is not None and validate_animation(file_name, present)[0]:
                sha = hashlib.sha256(present).hexdigest()
                return document, relative, len(present), sha, True
            if present is not None:
                print("[redo] cached %s failed validation; downloading again" % relative)
        async with worker_limiter:
            for attempt in range(6):
                data = None
                try:
                    data = await client.download_media(document, file=bytes)
                    break
                except errors.FloodWaitError as exc:
                    await asyncio.sleep(int(exc.seconds) + int(1.0 * attempt))
                except errors.RPCError as exc:
                    if attempt >= 5:
                        raise
                    await asyncio.sleep(2.0 * (attempt + 1))
                except (ConnectionError, TimeoutError, OSError):
                    if attempt >= 5:
                        raise
                    await asyncio.sleep(2.0 * (attempt + 1))
            else:
                raise RuntimeError("download document %d failed" % document.id)
        if not isinstance(data, (bytes, bytearray)):
            raise RuntimeError("document %d returned %r" % (document.id, type(data)))
        data = bytes(data)
        if len(data) != document.size:
            print(
                "[warn] document %d catalog size %d differs from actual %d; using actual"
                % (document.id, document.size, len(data))
            )
        relative = self.unique_path(document, data)
        self.write_atomic(relative, data)
        sha = hashlib.sha256(data).hexdigest()
        return document, relative, len(data), sha, False

    def write_atomic(self, relative, data):
        full = os.path.join(self.out_dir, relative)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        handle, temp_path = tempfile.mkstemp(dir=os.path.dirname(full), prefix=".tmp-")
        try:
            with os.fdopen(handle, "wb") as output:
                output.write(data)
            os.replace(temp_path, full)
        except BaseException:
            try:
                os.unlink(temp_path)
            except OSError:
                pass
            raise


async def collect_gift(index, gift, sink):
    if isinstance(gift, types.StarGiftUnique):
        return None
    if not isinstance(gift, types.StarGift):
        raise ValueError("unsupported gift constructor %r at index %d" % (gift, index))
    document = sink.register(gift.sticker, "gift:%d:sticker" % gift.id)
    manifest = {
        "index": index,
        "kind": "regular",
        "id": gift.id,
        "title": gift.title or "",
        "stars": int(gift.stars),
        "convert_stars": int(gift.convert_stars),
        "upgrade_stars": int(gift.upgrade_stars or 0),
        "resell_min_stars": int(gift.resell_min_stars or 0),
        "limited": bool(gift.limited),
        "sold_out": bool(gift.sold_out),
        "birthday": bool(gift.birthday),
        "require_premium": bool(gift.require_premium),
        "limited_per_user": bool(gift.limited_per_user),
        "peer_color_available": bool(gift.peer_color_available),
        "auction": bool(gift.auction),
        "availability_remains": int(gift.availability_remains or 0),
        "availability_total": int(gift.availability_total or 0),
        "availability_resale": int(gift.availability_resale or 0),
        "first_sale_date": unix_or_zero(gift.first_sale_date),
        "last_sale_date": unix_or_zero(gift.last_sale_date),
        "locked_until_date": unix_or_zero(gift.locked_until_date),
        "auction_slug": gift.auction_slug or "",
        "gifts_per_round": int(gift.gifts_per_round or 0),
        "auction_start_date": unix_or_zero(gift.auction_start_date),
        "upgrade_variants": int(gift.upgrade_variants or 0),
        "per_user_total": int(gift.per_user_total or 0),
        "per_user_remains": int(gift.per_user_remains or 0),
        "document_ids": [document.id],
    }
    background = background_manifest(gift.background)
    if background is not None:
        manifest["background"] = background
    return manifest


def upgradeable(gift):
    return isinstance(gift, types.StarGift) and gift.id and (
        (gift.upgrade_stars or 0) > 0 or (gift.upgrade_variants or 0) > 0
    )


async def fetch_upgrade_set(client, gift_id, sink):
    result = await client(functions.payments.GetStarGiftUpgradeAttributesRequest(gift_id))
    models, patterns, backdrops = [], [], []
    for attribute in result.attributes:
        if isinstance(attribute, types.StarGiftAttributeModel):
            sink.register(attribute.document, "gift:%d:upgrade-model:%s" % (gift_id, attribute.name))
            models.append({
                "name": attribute.name,
                "document_id": attribute.document.id,
                "crafted": bool(attribute.crafted),
                "rarity": rarity_manifest(attribute.rarity),
            })
        elif isinstance(attribute, types.StarGiftAttributePattern):
            sink.register(attribute.document, "gift:%d:upgrade-pattern:%s" % (gift_id, attribute.name))
            patterns.append({
                "name": attribute.name,
                "document_id": attribute.document.id,
                "rarity": rarity_manifest(attribute.rarity),
            })
        elif isinstance(attribute, types.StarGiftAttributeBackdrop):
            backdrops.append({
                "name": attribute.name,
                "backdrop_id": int(attribute.backdrop_id),
                "center_color": int(attribute.center_color),
                "edge_color": int(attribute.edge_color),
                "pattern_color": int(attribute.pattern_color),
                "text_color": int(attribute.text_color),
                "rarity": rarity_manifest(attribute.rarity),
            })
        else:
            raise ValueError("unsupported upgrade attribute %r" % (attribute,))
    if not models or not patterns or not backdrops:
        raise ValueError(
            "gift %d incomplete upgrade attributes: models=%d patterns=%d backdrops=%d"
            % (gift_id, len(models), len(patterns), len(backdrops))
        )
    if len(models) + len(patterns) + len(backdrops) != len(result.attributes):
        raise ValueError("gift %d attribute count mismatch" % gift_id)
    return {
        "gift_id": gift_id,
        "attribute_count": len(result.attributes),
        "models": models,
        "patterns": patterns,
        "backdrops": backdrops,
    }


def file_manifest(sink, downloaded):
    document, relative, size, sha, reused = downloaded
    file_name, _ = document_names(document)
    if not file_name:
        file_name = os.path.basename(relative)
    ok, error = validate_animation(file_name, open(os.path.join(sink.out_dir, relative), "rb").read())
    return {
        "id": document.id,
        "file_name": file_name,
        "file": {"path": relative, "size": size, "sha256": sha},
        "animation_validated": ok,
        "validation_error": error,
    }


def enforce_validation(documents, allow_invalid):
    invalid = [entry for entry in documents if not entry["animation_validated"]]
    if not invalid:
        print("[validate] all %d documents match the server animation rules" % len(documents))
        return
    for entry in invalid[:20]:
        print(
            "[validate] INVALID %s: %s" % (entry["file"]["path"], entry["validation_error"]),
            file=sys.stderr,
        )
    message = "%d document(s) would be rejected by the admin importer" % len(invalid)
    if allow_invalid:
        print("[validate] %s (kept because --allow-invalid)" % message, file=sys.stderr)
        return
    raise SystemExit(
        "[abort] %s; manifest.json was left untouched. Re-run with --force to "
        "redownload, or --allow-invalid to publish anyway." % message
    )


async def snapshot(client, out_dir, workers, max_bytes, force, allow_invalid=False):
    os.makedirs(out_dir, exist_ok=True)
    catalog = await client(functions.payments.GetStarGiftsRequest(0))
    if not isinstance(catalog, PaymentsStarGifts):
        raise TypeError("payments.getStarGifts(hash=0) returned %r" % (catalog,))
    if len(catalog.gifts) > MAX_CATALOG_GIFTS:
        raise ValueError("gift catalog has too many entries")

    sink = DocumentSink(out_dir, max_bytes, force)
    gifts = []
    upgrade_ids = []
    for index, gift in enumerate(catalog.gifts):
        parsed = await collect_gift(index, gift, sink)
        if parsed is None:
            print("[skip] non-regular gift at index %d" % index)
            continue
        gifts.append(parsed)
        if upgradeable(gift):
            upgrade_ids.append(gift.id)
    if not gifts:
        raise ValueError("catalog has no regular gifts")

    sets = []
    for gift_id in upgrade_ids:
        sets.append(await fetch_upgrade_set(client, gift_id, sink))
        print("[upgrade attributes] gift_id=%d" % gift_id)

    print("[documents discovered] count=%d" % len(sink.documents))
    limiter = asyncio.Semaphore(max(1, workers))
    downloaded = await asyncio.gather(
        *(sink.download(client, limiter, source) for source in sink.documents.values())
    )

    documents = [file_manifest(sink, entry) for entry in downloaded]
    documents.sort(key=lambda item: item["id"])
    enforce_validation(documents, allow_invalid)

    manifest = {
        "schema": MANIFEST_SCHEMA,
        "gift_count": len(gifts),
        "gifts": gifts,
        "upgrade_attribute_sets": sets,
        "documents": documents,
    }
    manifest_path = os.path.join(out_dir, "manifest.json")
    if os.path.exists(manifest_path):
        previous = json.load(open(manifest_path, encoding="utf-8"))
        previous_ids = {g["id"] for g in previous.get("gifts", [])}
        new_ids = {g["id"] for g in gifts}
        added = sorted(new_ids - previous_ids)
        removed = sorted(previous_ids - new_ids)
        if added:
            print("[delta] added official gifts: %s" % added)
        if removed:
            print("[delta] removed official gifts: %s" % removed)
        if added or removed:
            intact = len(previous_ids & new_ids)
            print("[delta] %d gifts unchanged" % intact)
        os.replace(manifest_path, os.path.join(out_dir, "manifest.previous.json"))
    encoded = json.dumps(manifest, ensure_ascii=False, separators=(",", ":"))
    with open(manifest_path, "w", encoding="utf-8") as handle:
        handle.write(encoded + "\n")
    total_bytes = sum(d["file"]["size"] for d in documents)
    print(
        "[complete] gifts=%d attribute_sets=%d documents=%d reused=%d bytes=%d manifest=%s"
        % (
            len(gifts),
            len(sets),
            len(documents),
            sum(1 for entry in downloaded if entry[4]),
            total_bytes,
            manifest_path,
        )
    )
    return manifest


async def ensure_login(client, phone):
    if await client.is_user_authorized():
        me = await client.get_me()
        print("[login] already authorized as %s (@%s)" % (me.first_name or "", me.username or ""))
        return
    if not phone:
        phone = input("Telegram phone (international format, e.g. +79990001122): ").strip()
    if not re.match(r"^\+[0-9]+$", phone):
        raise SystemExit("invalid phone number %r" % phone)
    sent = await client.send_code_request(phone)
    while True:
        code = input("Login code: ").strip()
        try:
            await client.sign_in(phone, code, phone_code_hash=sent.phone_code_hash)
            break
        except errors.SessionPasswordNeededError:
            for attempt in range(5):
                password = getpass.getpass("Two-factor password (2FA): ")
                try:
                    await client.sign_in(password=password)
                    break
                except errors.PasswordHashInvalidError:
                    print("Wrong 2FA password, try again.")
                except errors.TwoFaConfirmWaitError as exc:
                    print(exc.message)
                    await asyncio.sleep(int(exc.seconds))
                except errors.PhoneCodeInvalidError:
                    print("Login code rejected, retry.")
                    break
            else:
                raise SystemExit("2FA login failed after 5 attempts")
            break
        except errors.PhoneCodeInvalidError:
            print("Invalid login code, try again.")
        except errors.PhoneCodeExpiredError:
            raise SystemExit("Login code expired; rerun the script.")
    me = await client.get_me()
    print("[login] signed in as %s (@%s)" % (me.first_name or "", me.username or ""))


async def run(args):
    session = args.session
    session_string = os.environ.get("TELETHON_SESSION", "").strip()
    if session_string:
        session = sessions.StringSession(session_string)
    client = TelegramClient(
        session,
        args.api_id,
        args.api_hash,
        device_model="giftfetch-telethon",
        app_version="1.0",
        system_version="1.0",
        lang_code="en",
        system_lang_code="en",
    )
    await client.connect()
    try:
        await ensure_login(client, args.phone)
        await snapshot(client, args.out, args.workers, args.max_doc_bytes,
                       args.force, args.allow_invalid)
    finally:
        await client.disconnect()


def self_test(out_dir):
    """Build a synthetic snapshot and prove the validator rejects bad documents."""
    os.makedirs(out_dir, exist_ok=True)
    base_lottie = {
        "v": "5.7.4", "fr": 30, "ip": 0, "op": 30,
        "w": LOTTIE_WIDTH, "h": LOTTIE_HEIGHT,
        "layers": [{"ty": 4, "nm": "gift", "ip": 0, "op": 30}],
        "assets": [],
    }

    def tgs_bytes(payload):
        return gzip.compress(json.dumps(payload, separators=(",", ":")).encode("utf-8"))

    def tgs_doc(doc_id, alter=None):
        payload = dict(base_lottie)
        if alter:
            payload.update(alter)
        return tgs_bytes(payload), doc_id

    docs = {}
    docs[1001] = tgs_doc(1001)[0]
    docs[1002] = tgs_doc(1002)[0]
    docs[1003] = tgs_doc(1003, {"layers": []})[0]
    docs[1004] = tgs_doc(1004, {"w": 256, "h": 256})[0]
    docs[1005] = docs[1001] + b"\x00\x01"
    docs[2001] = tgs_doc(2001)[0]
    docs[2002] = tgs_doc(2002)[0]
    docs[2003] = tgs_doc(2003)[0]
    expected_invalid = {1003, 1004, 1005}
    expected_errors = {
        1003: "lottie has no layers",
        1004: "expected %dx%d" % (LOTTIE_WIDTH, LOTTIE_HEIGHT),
        1005: "single gzip",
    }

    def write_doc(doc_id):
        file_name = "%d.tgs" % doc_id
        relative = os.path.join("documents", file_name)
        full = os.path.join(out_dir, relative)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "wb") as handle:
            handle.write(docs[doc_id])
        ok, error = validate_animation(file_name, docs[doc_id])
        return {
            "id": doc_id,
            "file_name": file_name,
            "file": {
                "path": relative,
                "size": len(docs[doc_id]),
                "sha256": hashlib.sha256(docs[doc_id]).hexdigest(),
            },
            "animation_validated": ok,
            "validation_error": error,
        }

    all_ids = (1001, 1002, 1003, 1004, 1005, 2001, 2002, 2003)
    manifest = {
        "schema": MANIFEST_SCHEMA,
        "gift_count": 2,
        "gifts": [
            {
                "index": 0, "kind": "regular", "id": 9001, "title": "Self Test Gift One",
                "stars": 2000, "convert_stars": 1000, "upgrade_stars": 4500,
                "limited": True, "sold_out": False, "birthday": False,
                "require_premium": False, "limited_per_user": False,
                "peer_color_available": False, "auction": False,
                "availability_remains": 42, "availability_total": 100,
                "availability_resale": 0, "first_sale_date": 1700000000,
                "last_sale_date": 0, "resell_min_stars": 500,
                "per_user_total": 0, "per_user_remains": 0,
                "locked_until_date": 0, "auction_slug": "", "gifts_per_round": 0,
                "auction_start_date": 0, "upgrade_variants": 9,
                "background": {"center_color": 0xFF0000, "edge_color": 0x00FF00, "text_color": 0x0000FF},
                "document_ids": [1001],
            },
            {
                "index": 1, "kind": "regular", "id": 9002, "title": "Self Test Gift Two",
                "stars": 5000, "convert_stars": 2500, "upgrade_stars": 0,
                "limited": False, "sold_out": False, "birthday": True,
                "require_premium": False, "limited_per_user": False,
                "peer_color_available": False, "auction": False,
                "availability_remains": 0, "availability_total": 0,
                "availability_resale": 0, "first_sale_date": 0,
                "last_sale_date": 0, "resell_min_stars": 0,
                "per_user_total": 0, "per_user_remains": 0,
                "locked_until_date": 0, "auction_slug": "", "gifts_per_round": 0,
                "auction_start_date": 0, "upgrade_variants": 0,
                "document_ids": [1002],
            },
        ],
        "upgrade_attribute_sets": [
            {
                "gift_id": 9001,
                "attribute_count": 4,
                "models": [
                    {"name": "Amber", "document_id": 2001, "crafted": False,
                     "rarity": {"kind": "permille", "permille": 500}},
                    {"name": "Diamond Master", "document_id": 2002, "crafted": True,
                     "rarity": {"kind": "legendary"}},
                ],
                "patterns": [
                    {"name": "Stripes", "document_id": 2003,
                     "rarity": {"kind": "permille", "permille": 700}},
                ],
                "backdrops": [
                    {"name": "Sunset", "backdrop_id": 1, "center_color": 0xFFA500,
                     "edge_color": 0x808080, "pattern_color": 0x111111,
                     "text_color": 0xFFFFFF, "rarity": {"kind": "permille", "permille": 300}},
                ],
            }
        ],
        "documents": [write_doc(doc_id) for doc_id in all_ids],
    }
    manifest_path = os.path.join(out_dir, "manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as handle:
        handle.write(json.dumps(manifest, ensure_ascii=False, separators=(",", ":")) + "\n")

    actual_invalid = {d["id"] for d in manifest["documents"] if not d["animation_validated"]}
    failures = []
    if actual_invalid != expected_invalid:
        failures.append("invalid set %s, expected %s" % (sorted(actual_invalid), sorted(expected_invalid)))
    for doc_id, fragment in expected_errors.items():
        entry = next(d for d in manifest["documents"] if d["id"] == doc_id)
        if fragment not in entry["validation_error"]:
            failures.append("doc %d error %r lacks %r" % (doc_id, entry["validation_error"], fragment))
    print("[self-test] synthetic snapshot written to %s" % out_dir)
    print("[self-test] rejected as expected: %s" % sorted(actual_invalid))
    if failures:
        for failure in failures:
            print("[self-test] FAIL %s" % failure, file=sys.stderr)
        return 1
    print("[self-test] validator matches the server rules")
    return 0


def main():
    parser = argparse.ArgumentParser(description="Download official star gifts for gramsrv via Telethon")
    parser.add_argument("--api-id", type=int, default=API_ID)
    parser.add_argument("--api-hash", default=API_HASH)
    parser.add_argument("--session", default="giftfetch.session", help="Telethon session file")
    parser.add_argument("--phone", default="", help="phone to log in (optional; prompts otherwise)")
    parser.add_argument("--out", default="./giftfetch-out", help="output snapshot directory")
    parser.add_argument("--workers", type=int, default=DEFAULT_WORKERS)
    parser.add_argument("--max-doc-bytes", type=int, default=MAX_DOC_BYTES)
    parser.add_argument("--force", action="store_true", help="redownload documents even if cached")
    parser.add_argument("--allow-invalid", action="store_true",
                        help="publish the manifest even when documents fail animation validation")
    parser.add_argument("--self-test", action="store_true",
                        help="write a synthetic snapshot, check the validator against it and exit")
    args = parser.parse_args()
    if args.self_test:
        return self_test(args.out)
    asyncio.run(run(args))
    return 0


if __name__ == "__main__":
    sys.exit(main())
