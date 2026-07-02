import type { FormEvent } from "react";
import { useState } from "react";
import { api, errorMessage } from "../api";
import { Alert } from "../components/ui";
import { LanguageSwitch, useI18n } from "../i18n";

export function LoginPage({ onLogin }: { onLogin: (actor: string) => void }) {
  const { t } = useI18n();
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = await api.login(secret);
      onLogin(result.actor);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="login-page">
      <section className="login-panel">
        <div className="login-head">
          <div className="brand brand-elevated">
            <span className="brand-mark">T</span>
            <span>
              <strong>telesrv</strong>
              <small>{t("app.adminConsole")}</small>
            </span>
          </div>
          <div className="login-head-actions">
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
            <span>{t("login.secret")}</span>
            <input
              autoFocus
              type="password"
              value={secret}
              autoComplete="current-password"
              onChange={(event) => setSecret(event.target.value)}
            />
          </label>
          <button className="btn primary full" type="submit" disabled={busy}>
            {busy ? t("login.submitting") : t("login.submit")}
          </button>
        </form>
      </section>
    </main>
  );
}
