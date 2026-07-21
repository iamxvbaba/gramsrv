"""Local Bedolaga Telegram Login/OIDC relying-party demo.

This is deliberately a relying party, not a shortcut into telesrv internals. It
validates the three public contracts used by a Bedolaga-style bot:

* Bot API ``login_url`` legacy HMAC callbacks;
* the self-hosted Telegram Login JavaScript SDK ``post_message`` response; and
* confidential authorization-code + PKCE followed by JWKS ID-token validation.

The demo keeps its short-lived browser flows in memory. It is intended for
local/end-to-end verification only and must not be used as a production login
backend.
"""

from __future__ import annotations

import asyncio
import base64
from dataclasses import dataclass
import hashlib
import hmac
import html
import json
import logging
import secrets
import time
from typing import Any
from urllib.parse import urlencode, urlsplit

from aiohttp import BasicAuth, ClientSession, ClientTimeout, web
import jwt


LOG = logging.getLogger("bedolagaformat.login")
FLOW_TTL_SECONDS = 10 * 60
MAX_PENDING_FLOWS = 256
LEGACY_AUTH_MAX_AGE_SECONDS = 15 * 60
OIDC_ALGORITHMS = ("RS256", "ES256", "EdDSA", "ES256K")


@dataclass(frozen=True)
class LoginDemoConfig:
    issuer: str
    client_id: str
    client_secret: str
    public_url: str
    listen_host: str
    listen_port: int

    @property
    def redirect_uri(self) -> str:
        return self.public_url + "/oauth/callback"

    @property
    def origin(self) -> str:
        parsed = urlsplit(self.public_url)
        return f"{parsed.scheme}://{parsed.netloc}"

    @property
    def code_flow_enabled(self) -> bool:
        return bool(self.client_secret)


@dataclass(frozen=True)
class PendingFlow:
    nonce: str
    expires_at: float
    code_verifier: str = ""


def normalize_web_base(value: str, *, name: str) -> str:
    raw = value.strip().rstrip("/")
    parsed = urlsplit(raw)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.netloc
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in {"", "/"}
    ):
        raise ValueError(f"{name} must be an absolute origin without path, query, or fragment")
    return f"{parsed.scheme}://{parsed.netloc}"


def parse_listen(value: str) -> tuple[str, int]:
    parsed = urlsplit("//" + value.strip())
    try:
        port = parsed.port
    except ValueError as exc:
        raise ValueError("login demo listen port is invalid") from exc
    if not parsed.hostname or port is None or not 1 <= port <= 65535:
        raise ValueError("login demo listen address must be host:port")
    return parsed.hostname, port


def base64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def generate_pkce() -> tuple[str, str]:
    verifier = base64url(secrets.token_bytes(32))
    challenge = base64url(hashlib.sha256(verifier.encode("ascii")).digest())
    return verifier, challenge


def verify_legacy_login_query(
    query: dict[str, str], bot_token: str, *, now: int | None = None
) -> dict[str, str]:
    """Verify Telegram's legacy login_url data-check string.

    Only the documented signed identity fields participate. Query parameters
    already present on the relying-party URL are intentionally excluded.
    """

    signed_names = (
        "auth_date",
        "first_name",
        "id",
        "last_name",
        "photo_url",
        "username",
    )
    supplied_hash = query.get("hash", "")
    if len(supplied_hash) != 64:
        raise ValueError("missing legacy login signature")
    values = {name: query[name] for name in signed_names if name in query}
    if not all(values.get(name) for name in ("auth_date", "first_name", "id")):
        raise ValueError("incomplete legacy login payload")
    try:
        auth_date = int(values["auth_date"])
        user_id = int(values["id"])
    except ValueError as exc:
        raise ValueError("invalid legacy login payload") from exc
    current = int(time.time()) if now is None else now
    if user_id <= 0 or auth_date > current + 30 or current - auth_date > LEGACY_AUTH_MAX_AGE_SECONDS:
        raise ValueError("expired legacy login payload")
    data_check = "\n".join(f"{name}={values[name]}" for name in sorted(values))
    secret_key = hashlib.sha256(bot_token.encode("utf-8")).digest()
    actual = hmac.new(secret_key, data_check.encode("utf-8"), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(actual, supplied_hash.lower()):
        raise ValueError("invalid legacy login signature")
    return values


def _safe_claims(claims: dict[str, Any]) -> dict[str, Any]:
    allowed = (
        "iss",
        "aud",
        "sub",
        "iat",
        "exp",
        "nonce",
        "id",
        "name",
        "given_name",
        "family_name",
        "preferred_username",
        "picture",
        "phone_number",
        "phone_number_verified",
    )
    return {key: claims[key] for key in allowed if key in claims}


class LoginDemoServer:
    def __init__(self, config: LoginDemoConfig, bot_token: str) -> None:
        self.config = config
        self.bot_token = bot_token
        self._flows: dict[str, PendingFlow] = {}
        self._flow_lock = asyncio.Lock()
        self._http: ClientSession | None = None
        self._runner: web.AppRunner | None = None

    async def start(self) -> None:
        timeout = ClientTimeout(total=10)
        self._http = ClientSession(timeout=timeout)
        app = web.Application(client_max_size=64 * 1024)
        app.add_routes(
            [
                web.get("/", self.root),
                web.get("/login/code", self.start_code_flow),
                web.get("/oauth/callback", self.code_callback),
                web.post("/verify-popup", self.verify_popup),
                web.get("/healthz", self.health),
            ]
        )
        self._runner = web.AppRunner(app, access_log=None)
        await self._runner.setup()
        site = web.TCPSite(self._runner, self.config.listen_host, self.config.listen_port)
        await site.start()
        LOG.info(
            "Telegram Login demo listening at %s (issuer=%s client_id=%s)",
            self.config.public_url,
            self.config.issuer,
            self.config.client_id,
        )

    async def close(self) -> None:
        if self._runner is not None:
            await self._runner.cleanup()
            self._runner = None
        if self._http is not None:
            await self._http.close()
            self._http = None

    async def _put_flow(self, flow: PendingFlow) -> str:
        flow_id = secrets.token_urlsafe(24)
        now = time.time()
        async with self._flow_lock:
            self._flows = {
                key: value for key, value in self._flows.items() if value.expires_at > now
            }
            if len(self._flows) >= MAX_PENDING_FLOWS:
                oldest = min(self._flows, key=lambda key: self._flows[key].expires_at)
                del self._flows[oldest]
            self._flows[flow_id] = flow
        return flow_id

    async def _take_flow(self, flow_id: str, *, consume: bool) -> PendingFlow:
        async with self._flow_lock:
            flow = self._flows.get(flow_id)
            if flow is None or flow.expires_at <= time.time():
                self._flows.pop(flow_id, None)
                raise ValueError("login flow is invalid or expired")
            if consume:
                del self._flows[flow_id]
            return flow

    @staticmethod
    def _headers(response: web.StreamResponse) -> None:
        response.headers["Cache-Control"] = "no-store"
        response.headers["Pragma"] = "no-cache"
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["Referrer-Policy"] = "no-referrer"

    async def health(self, _: web.Request) -> web.Response:
        response = web.json_response({"status": "ok"})
        self._headers(response)
        return response

    async def root(self, request: web.Request) -> web.Response:
        legacy_result = ""
        if "hash" in request.query:
            try:
                signed_names = {
                    "auth_date", "first_name", "id", "last_name", "photo_url", "username", "hash"
                }
                if any(len(request.query.getall(key, [])) != 1 for key in signed_names if key in request.query):
                    raise ValueError("duplicate legacy login field")
                query = {key: request.query[key] for key in request.query}
                identity = verify_legacy_login_query(query, self.bot_token)
                legacy_result = (
                    "<p class=\"ok\">login_url HMAC verified for user "
                    + html.escape(identity["id"])
                    + ".</p>"
                )
            except ValueError:
                legacy_result = '<p class="error">login_url HMAC verification failed.</p>'

        nonce = secrets.token_urlsafe(24)
        flow_id = await self._put_flow(PendingFlow(nonce=nonce, expires_at=time.time() + FLOW_TTL_SECONDS))
        sdk_url = self.config.issuer + "/js/telegram-login.js"
        csp_nonce = secrets.token_urlsafe(18)
        code_link = '<a class="button" href="/login/code">Authorization Code + PKCE</a>'
        if not self.config.code_flow_enabled:
            code_link = '<span class="disabled">Code flow disabled: configure client secret.</span>'
        script_config = json.dumps(
            {"clientID": self.config.client_id, "flowID": flow_id, "nonce": nonce},
            separators=(",", ":"),
        ).replace("<", "\\u003c")
        body = f"""<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Bedolaga Telegram Login Demo</title>
<style>body{{font:16px/1.5 system-ui,sans-serif;max-width:720px;margin:8vh auto;padding:24px;background:#17212b;color:#fff}}.card{{background:#202b36;padding:28px;border-radius:18px}}button,.button{{display:inline-block;border:0;border-radius:10px;padding:12px 18px;margin:8px 8px 8px 0;background:#2aabee;color:#fff;text-decoration:none;font-weight:700;cursor:pointer}}.disabled{{display:block;color:#a9b5c1;margin:12px 0}}pre{{white-space:pre-wrap;overflow-wrap:anywhere;background:#111b24;padding:14px;border-radius:8px}}.ok{{color:#73d18a}}.error{{color:#ff8d8d}}</style></head>
<body><main class="card"><h1>Bedolaga Telegram Login Demo</h1>{legacy_result}
<p>This page verifies the self-hosted JavaScript SDK and the standard server-side OIDC flow.</p>
<button id="popup">JavaScript SDK popup</button>{code_link}<pre id="result">Ready.</pre></main>
<script src="{html.escape(sdk_url, quote=True)}"></script>
<script nonce="{csp_nonce}">const cfg={script_config},out=document.getElementById('result');document.getElementById('popup').addEventListener('click',()=>{{out.textContent='Waiting for Telegram approval…';Telegram.Login.auth({{client_id:cfg.clientID,request_access:['phone','write'],nonce:cfg.nonce}},async result=>{{if(result.error){{out.textContent='Login failed: '+result.error;return}}try{{const response=await fetch('/verify-popup',{{method:'POST',headers:{{'content-type':'application/json'}},body:JSON.stringify({{flow_id:cfg.flowID,id_token:result.id_token,in_app:Boolean(window.TelegramWebviewProxy)}})}}),verified=await response.json();if(!response.ok)throw new Error(verified.error||'verification_failed');out.textContent=JSON.stringify(verified.claims,null,2)}}catch(error){{out.textContent='Verification failed: '+error.message}}}})}});</script></body></html>"""
        response = web.Response(text=body, content_type="text/html")
        self._headers(response)
        response.headers["Content-Security-Policy"] = (
            "default-src 'none'; style-src 'unsafe-inline'; "
            f"script-src '{csp_nonce}' {self.config.issuer}; connect-src 'self' {self.config.issuer}; "
            "frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
        )
        # CSP nonce source expressions include the nonce- prefix.
        response.headers["Content-Security-Policy"] = response.headers["Content-Security-Policy"].replace(
            f"'{csp_nonce}'", f"'nonce-{csp_nonce}'"
        )
        return response

    async def start_code_flow(self, _: web.Request) -> web.Response:
        if not self.config.code_flow_enabled:
            raise web.HTTPNotFound()
        verifier, challenge = generate_pkce()
        nonce = secrets.token_urlsafe(24)
        state = await self._put_flow(
            PendingFlow(nonce=nonce, code_verifier=verifier, expires_at=time.time() + FLOW_TTL_SECONDS)
        )
        query = urlencode(
            {
                "client_id": self.config.client_id,
                "redirect_uri": self.config.redirect_uri,
                "response_type": "code",
                "scope": "openid profile phone telegram:bot_access",
                "state": state,
                "nonce": nonce,
                "code_challenge": challenge,
                "code_challenge_method": "S256",
            }
        )
        response = web.HTTPFound(self.config.issuer + "/auth?" + query)
        self._headers(response)
        raise response

    async def code_callback(self, request: web.Request) -> web.Response:
        state = request.query.get("state", "")
        try:
            flow = await self._take_flow(state, consume=True)
        except ValueError:
            return self._result_page("Authorization failed", {"error": "invalid_or_expired_state"}, ok=False)
        if request.query.get("error"):
            return self._result_page("Authorization declined", {"error": request.query["error"]}, ok=False)
        code = request.query.get("code", "")
        if not code or len(code) > 2048:
            return self._result_page("Authorization failed", {"error": "missing_code"}, ok=False)
        try:
            token = await self._exchange_code(code, flow.code_verifier)
            claims = await self.verify_id_token(token, flow.nonce)
        except Exception as exc:  # noqa: BLE001 - convert all protocol failures to a safe demo page
            LOG.warning("OIDC code flow verification failed: %s", type(exc).__name__)
            return self._result_page("Authorization failed", {"error": "token_verification_failed"}, ok=False)
        return self._result_page("Authorization complete", _safe_claims(claims), ok=True)

    async def verify_popup(self, request: web.Request) -> web.Response:
        origin = request.headers.get("Origin")
        if origin and origin != self.config.origin:
            return web.json_response({"error": "invalid_origin"}, status=403)
        try:
            payload = await request.json()
            flow_id = str(payload.get("flow_id", ""))
            id_token = str(payload.get("id_token", ""))
            in_app = payload.get("in_app", False)
            if (
                not flow_id
                or len(flow_id) > 128
                or not id_token
                or len(id_token) > 16384
                or not isinstance(in_app, bool)
            ):
                raise ValueError("invalid popup response")
            flow = await self._take_flow(flow_id, consume=True)
            # Telegram's official Mini App /inapp contract has exactly four
            # request parameters and does not carry the JS API nonce. Popup
            # flows still require it; Mini App tokens must omit it.
            claims = await self.verify_id_token(id_token, "" if in_app else flow.nonce)
        except Exception as exc:  # noqa: BLE001 - safe public validation failure
            LOG.info("OIDC popup verification rejected: %s", type(exc).__name__)
            response = web.json_response({"error": "token_verification_failed"}, status=400)
            self._headers(response)
            return response
        response = web.json_response({"claims": _safe_claims(claims)})
        self._headers(response)
        return response

    async def _discovery(self) -> dict[str, Any]:
        if self._http is None:
            raise RuntimeError("login demo server is not started")
        async with self._http.get(self.config.issuer + "/.well-known/openid-configuration") as response:
            response.raise_for_status()
            document = await response.json()
        if document.get("issuer") != self.config.issuer:
            raise ValueError("OIDC issuer mismatch")
        issuer_origin = urlsplit(self.config.issuer).netloc
        for name in ("token_endpoint", "jwks_uri"):
            endpoint = urlsplit(str(document.get(name, "")))
            if endpoint.scheme != urlsplit(self.config.issuer).scheme or endpoint.netloc != issuer_origin:
                raise ValueError(f"OIDC {name} must share the configured issuer origin")
        return document

    async def _exchange_code(self, code: str, verifier: str) -> str:
        if self._http is None:
            raise RuntimeError("login demo server is not started")
        discovery = await self._discovery()
        form = {
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": self.config.redirect_uri,
            "code_verifier": verifier,
        }
        async with self._http.post(
            discovery["token_endpoint"],
            data=form,
            auth=BasicAuth(self.config.client_id, self.config.client_secret),
        ) as response:
            document = await response.json()
            if response.status != 200:
                raise ValueError("OIDC token endpoint rejected the grant")
        token = document.get("id_token")
        if not isinstance(token, str) or not token:
            raise ValueError("OIDC token response omitted id_token")
        return token

    async def verify_id_token(self, token: str, nonce: str) -> dict[str, Any]:
        if self._http is None:
            raise RuntimeError("login demo server is not started")
        discovery = await self._discovery()
        header = jwt.get_unverified_header(token)
        kid, algorithm = header.get("kid"), header.get("alg")
        if not isinstance(kid, str) or algorithm not in OIDC_ALGORITHMS:
            raise ValueError("unsupported ID token header")
        async with self._http.get(discovery["jwks_uri"]) as response:
            response.raise_for_status()
            document = await response.json()
        raw_key = next((key for key in document.get("keys", []) if key.get("kid") == kid), None)
        if raw_key is None:
            raise ValueError("ID token signing key not found")
        public_key = jwt.PyJWK.from_dict(raw_key, algorithm=algorithm).key
        required_claims = ["iss", "aud", "sub", "iat", "exp"]
        if nonce:
            required_claims.append("nonce")
        claims = jwt.decode(
            token,
            public_key,
            algorithms=[algorithm],
            audience=self.config.client_id,
            issuer=self.config.issuer,
            options={"require": required_claims},
        )
        if nonce and not hmac.compare_digest(str(claims.get("nonce", "")), nonce):
            raise ValueError("ID token nonce mismatch")
        if not nonce and claims.get("nonce") not in (None, ""):
            raise ValueError("unexpected ID token nonce")
        if str(claims.get("sub", "")) != str(claims.get("id", "")):
            raise ValueError("ID token subject mismatch")
        return claims

    def _result_page(self, title: str, payload: dict[str, Any], *, ok: bool) -> web.Response:
        css_class = "ok" if ok else "error"
        body = (
            '<!doctype html><html lang="en"><head><meta charset="utf-8"><title>'
            + html.escape(title)
            + "</title></head><body><h1 class=\""
            + css_class
            + "\">"
            + html.escape(title)
            + "</h1><pre>"
            + html.escape(json.dumps(payload, indent=2, ensure_ascii=False))
            + '</pre><p><a href="/">Run another flow</a></p></body></html>'
        )
        response = web.Response(text=body, content_type="text/html")
        self._headers(response)
        response.headers["Content-Security-Policy"] = "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'"
        return response
