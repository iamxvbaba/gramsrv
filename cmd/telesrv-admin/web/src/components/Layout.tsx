import {
  ChevronDown,
  Database,
  LayoutDashboard,
  LogOut,
  MessageSquareText,
  Server,
  Shield,
  ShieldCheck,
  Users
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api";
import { LanguageSwitch, useI18n } from "../i18n";
import { type Navigate, type RouteState, routeSubtitle, routeTitle } from "../routing";
import { AppLink } from "./AppLink";

export function BootScreen() {
  const { t } = useI18n();
  return (
    <div className="boot-screen">
      <div className="brand compact brand-elevated">
        <span className="brand-mark">T</span>
        <span>
          <strong>telesrv</strong>
          <small>{t("app.adminConsole")}</small>
        </span>
      </div>
      <div className="loader-bar" />
    </div>
  );
}

export function Shell({
  actor,
  route,
  navigate,
  onLogout,
  children
}: {
  actor: string;
  route: RouteState;
  navigate: Navigate;
  onLogout: () => void;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const messagesActive = route.path.startsWith("/messages");
  const [messagesOpen, setMessagesOpen] = useState(messagesActive);

  useEffect(() => {
    if (messagesActive) {
      setMessagesOpen(true);
    }
  }, [messagesActive]);

  async function logout() {
    await api.logout().catch(() => undefined);
    onLogout();
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <AppLink className="brand" href="/" navigate={navigate}>
          <span className="brand-mark">T</span>
          <span>
            <strong>telesrv</strong>
            <small>{t("app.adminConsole")}</small>
          </span>
        </AppLink>
        <div className="sidebar-label">{t("layout.navigation")}</div>
        <nav className="nav-list" aria-label={t("layout.primaryNav")}>
          <NavLink icon={<LayoutDashboard size={16} />} href="/" route={route} navigate={navigate}>{t("layout.dashboard")}</NavLink>
          <NavLink icon={<Users size={16} />} href="/accounts" route={route} navigate={navigate}>{t("layout.accounts")}</NavLink>
          <NavLink icon={<ShieldCheck size={16} />} href="/channels" route={route} navigate={navigate}>{t("layout.channels")}</NavLink>
          <div className={`nav-section ${messagesActive ? "active" : ""} ${messagesOpen ? "open" : ""}`}>
            <button
              className="nav-section-toggle"
              type="button"
              aria-expanded={messagesOpen}
              onClick={() => setMessagesOpen((open) => !open)}
            >
              <MessageSquareText size={16} />
              <span>{t("layout.messages")}</span>
              <ChevronDown className="nav-section-chevron" size={15} />
            </button>
            {messagesOpen && (
              <div className="nav-children">
                <NavLink
                  href="/messages/private"
                  route={route}
                  navigate={navigate}
                  activeWhen={(path) => path === "/messages" || path === "/messages/detail" || path.startsWith("/messages/private")}
                >
                  {t("layout.privateMessages")}
                </NavLink>
                <NavLink
                  href="/messages/groups"
                  route={route}
                  navigate={navigate}
                  activeWhen={(path) => path.startsWith("/messages/groups")}
                >
                  {t("layout.groupMessages")}
                </NavLink>
              </div>
            )}
          </div>
        </nav>
        <div className="sidebar-status">
          <div className="sidebar-label">{t("layout.runtime")}</div>
          <div className="runtime-row"><Server size={14} /><span>{t("layout.adminBackend")}</span><strong>{t("layout.ready")}</strong></div>
          <div className="runtime-row"><Database size={14} /><span>{t("layout.pgRead")}</span><strong>{t("layout.readOnly")}</strong></div>
          <div className="runtime-row"><Shield size={14} /><span>{t("layout.writeOps")}</span><strong>{t("layout.dryRun")}</strong></div>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <div>
            <div className="eyebrow">{routeSubtitle(route.path, t)}</div>
            <h1>{routeTitle(route.path, t)}</h1>
          </div>
          <div className="topbar-actions">
            <LanguageSwitch />
            <span className="actor-pill">{t("layout.actor", { actor })}</span>
            <button className="btn ghost icon-text" type="button" onClick={logout} title={t("layout.logout")}>
              <LogOut size={16} /> {t("layout.logout")}
            </button>
          </div>
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  );
}

function NavLink({
  href,
  route,
  navigate,
  icon,
  children,
  activeWhen
}: {
  href: string;
  route: RouteState;
  navigate: Navigate;
  icon?: ReactNode;
  children: ReactNode;
  activeWhen?: (path: string) => boolean;
}) {
  const active = activeWhen ? activeWhen(route.path) : href === "/" ? route.path === "/" : route.path.startsWith(href);
  return (
    <AppLink className={`nav-item ${active ? "active" : ""}`} href={href} navigate={navigate}>
      {icon ?? <span aria-hidden="true" className="nav-dot" />}
      <span>{children}</span>
    </AppLink>
  );
}
