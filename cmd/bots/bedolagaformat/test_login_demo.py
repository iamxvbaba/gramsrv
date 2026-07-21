import asyncio
import hashlib
import hmac
from pathlib import Path
import sys
import time
import unittest

from aiohttp import ClientSession, web
from aiohttp.test_utils import TestServer
from cryptography.hazmat.primitives.asymmetric import rsa
import jwt


sys.path.insert(0, str(Path(__file__).parent))
import login_demo as demo  # noqa: E402


class LoginDemoHelpersTest(unittest.TestCase):
    def test_legacy_login_hmac_and_freshness(self) -> None:
        now = 1_800_000_000
        token = "9001:bot-secret"
        values = {
            "auth_date": str(now - 10),
            "first_name": "Alice",
            "id": "42",
            "username": "alice",
        }
        data_check = "\n".join(f"{key}={values[key]}" for key in sorted(values))
        key = hashlib.sha256(token.encode()).digest()
        values["hash"] = hmac.new(key, data_check.encode(), hashlib.sha256).hexdigest()
        values["untrusted_existing_query"] = "not-signed"

        verified = demo.verify_legacy_login_query(values, token, now=now)

        self.assertEqual(verified["id"], "42")
        self.assertNotIn("untrusted_existing_query", verified)
        with self.assertRaisesRegex(ValueError, "signature"):
            demo.verify_legacy_login_query({**values, "id": "43"}, token, now=now)
        with self.assertRaisesRegex(ValueError, "expired"):
            demo.verify_legacy_login_query(values, token, now=now + 3600)

    def test_web_origins_and_listen_are_strict(self) -> None:
        self.assertEqual(
            demo.normalize_web_base("https://rp.example/", name="RP"),
            "https://rp.example",
        )
        self.assertEqual(
            demo.normalize_web_base("http://127.0.0.1:3000", name="RP"),
            "http://127.0.0.1:3000",
        )
        self.assertEqual(
            demo.normalize_web_base("http://192.0.2.25:3000", name="RP"),
            "http://192.0.2.25:3000",
        )
        self.assertEqual(
            demo.normalize_web_base("http://rp.example:18080", name="RP"),
            "http://rp.example:18080",
        )
        with self.assertRaises(ValueError):
            demo.normalize_web_base("https://rp.example/callback", name="RP")
        self.assertEqual(demo.parse_listen("127.0.0.1:3000"), ("127.0.0.1", 3000))


class LoginDemoTokenTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        raw_jwk = jwt.algorithms.RSAAlgorithm.to_jwk(self.private_key.public_key(), as_dict=True)
        raw_jwk.update({"kid": "demo-rs256", "alg": "RS256", "use": "sig"})
        self.jwk = raw_jwk
        self.issuer = ""

        async def discovery(_: web.Request) -> web.Response:
            return web.json_response(
                {
                    "issuer": self.issuer,
                    "token_endpoint": self.issuer + "/token",
                    "jwks_uri": self.issuer + "/jwks",
                }
            )

        async def jwks(_: web.Request) -> web.Response:
            return web.json_response({"keys": [self.jwk]})

        app = web.Application()
        app.add_routes([web.get("/.well-known/openid-configuration", discovery), web.get("/jwks", jwks)])
        self.http_server = TestServer(app)
        await self.http_server.start_server()
        self.issuer = str(self.http_server.make_url("")).rstrip("/")
        config = demo.LoginDemoConfig(
            issuer=self.issuer,
            client_id="9001",
            client_secret="secret",
            public_url="http://127.0.0.1:3000",
            listen_host="127.0.0.1",
            listen_port=3000,
        )
        self.demo = demo.LoginDemoServer(config, "9001:bot-secret")
        self.demo._http = ClientSession()

    async def asyncTearDown(self) -> None:
        await self.demo._http.close()
        await self.http_server.close()

    async def test_id_token_requires_signature_issuer_audience_nonce_and_subject(self) -> None:
        now = int(time.time())
        claims = {
            "iss": self.issuer,
            "aud": "9001",
            "sub": "42",
            "id": 42,
            "iat": now,
            "exp": now + 300,
            "nonce": "expected-nonce",
            "name": "Alice",
        }
        token = jwt.encode(
            claims,
            self.private_key,
            algorithm="RS256",
            headers={"kid": "demo-rs256"},
        )

        verified = await self.demo.verify_id_token(token, "expected-nonce")

        self.assertEqual(verified["sub"], "42")
        with self.assertRaisesRegex(ValueError, "nonce"):
            await self.demo.verify_id_token(token, "wrong-nonce")
        with self.assertRaisesRegex(ValueError, "nonce"):
            await self.demo.verify_id_token(token, "")

        in_app_claims = dict(claims)
        in_app_claims.pop("nonce")
        in_app_token = jwt.encode(
            in_app_claims,
            self.private_key,
            algorithm="RS256",
            headers={"kid": "demo-rs256"},
        )
        verified_in_app = await self.demo.verify_id_token(in_app_token, "")
        self.assertEqual(verified_in_app["sub"], "42")

    async def test_pending_flow_is_one_time_and_expiring(self) -> None:
        flow_id = await self.demo._put_flow(
            demo.PendingFlow(nonce="n", expires_at=time.time() + 10)
        )
        flow = await self.demo._take_flow(flow_id, consume=True)
        self.assertEqual(flow.nonce, "n")
        with self.assertRaises(ValueError):
            await self.demo._take_flow(flow_id, consume=True)

        expired = await self.demo._put_flow(
            demo.PendingFlow(nonce="old", expires_at=time.time() - 1)
        )
        with self.assertRaises(ValueError):
            await self.demo._take_flow(expired, consume=True)


if __name__ == "__main__":
    unittest.main()
