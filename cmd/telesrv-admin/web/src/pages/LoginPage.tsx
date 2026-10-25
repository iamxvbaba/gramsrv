import type { FormEvent } from "react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert } from "../components/ui";
import { AppBackground } from "../components/AppBackground";
import { LanguageSwitch, useI18n } from "../i18n";
import { ThemeSwitch } from "../theme";
import type { AdminSession } from "../types";
import { withBase } from "../lib/base";

export function LoginPage({ onLogin }: { onLogin: (session: AdminSession) => void }) {
  const { t } = useI18n();
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // The operator's own icon, fetched from the public route so the login screen
  // shows the same mark the sidebar does instead of the bundled reference
  // logo. A 404 (nothing configured) is the expected case, not an error worth
  // showing anyone, so it just leaves the fallback in place.
  const [brandIcon, setBrandIcon] = useState(withBase("/logo.png"));

  useEffect(() => {
    const probe = new Image();
    probe.onload = () => setBrandIcon(api.publicServerIconURL());
    probe.onerror = () => undefined;
    probe.src = api.publicServerIconURL();
  }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      // The login answer carries the permission set and the CSRF token; api.login
      // remembers the token, the session state keeps the rights.
      const result = await api.login(username, secret);
      onLogin({ actor: result.actor, permissions: result.permissions ?? [] });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="login-page">
      <AppBackground />
      <section className="login-panel">
        <div className="login-head">
          <div className="brand brand-elevated">
            <span className="brand-mark"><img src={brandIcon} alt="" /></span>
            <span>
              <strong>telesrv</strong>
              <small>{t("app.adminConsole")}</small>
            </span>
          </div>
          <div className="login-head-actions">
            <ThemeSwitch />
            <LanguageSwitch />
            <span className="login-chip">{t("app.localAccess")}</span>
          </div>
        </div>
        <div className="login-copy">
          <h1>{t("login.heading")}</h1>
          <p>{t("login.body")}</p>
        </div>
        {error && <Alert>{error}</Alert>}
        <form className="form-stack" onSubmit={submit}>
          <label>
            <span>{t("login.username")}</span>
            <input
              autoFocus
              type="text"
              value={username}
              autoComplete="username"
              spellCheck={false}
              onChange={(event) => setUsername(event.target.value)}
            />
          </label>
          <label>
            <span>{t("login.secret")}</span>
            <input
              type="password"
              value={secret}
              autoComplete="current-password"
              onChange={(event) => setSecret(event.target.value)}
            />
          </label>
          <button className="btn primary full" type="submit" disabled={busy || !username.trim()}>
            {busy ? t("login.submitting") : t("login.submit")}
          </button>
        </form>
      </section>
    </main>
  );
}
