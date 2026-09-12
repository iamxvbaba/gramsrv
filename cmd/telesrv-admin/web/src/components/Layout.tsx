import {
  AtSign,
  BadgeCheck,
  Bot,
  ChevronDown,
  Database,
  Gavel,
	Film,
  LayoutDashboard,
  LogOut,
  MessageSquareText,
  Megaphone,
  Phone,
  BadgeDollarSign,
  Rss,
  Server,
  Shield,
  ShieldAlert,
  ShieldCheck,
  Smile,
  Stamp,
  Sticker,
  Trophy,
  Users,
	Gift,
	Send
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api";
import { LanguageSwitch, useI18n } from "../i18n";
import { permissionBotVerificationReview, permissionPremiumManage, permissionVerificationReview, useCan } from "../permissions";
import { type Navigate, type RouteState, routeSubtitle, routeTitle } from "../routing";
import { ThemeSwitch } from "../theme";
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
  // The verification queue is hidden for a session without verification.review:
  // the entry would only lead to a 403 (and the route itself is gated as well).
  const canReviewVerification = useCan(permissionVerificationReview);
  // Same reasoning for the third-party queue, which has its own right: the two
  // sections are granted independently, so one entry can be visible without the other.
  const canReviewBotVerification = useCan(permissionBotVerificationReview);
  const canManagePremium = useCan(permissionPremiumManage);
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
          <NavLink icon={<Bot size={16} />} href="/bots" route={route} navigate={navigate}>{t("layout.bots")}</NavLink>
          <NavLink icon={<Megaphone size={16} />} href="/broadcasts" route={route} navigate={navigate}>{t("layout.broadcasts")}</NavLink>
          {canManagePremium && (
            <NavLink icon={<BadgeDollarSign size={16} />} href="/monetization" route={route} navigate={navigate}
              activeWhen={(path) => path.startsWith("/monetization") || path.startsWith("/premium")}>
              {t("layout.premium")}
            </NavLink>
          )}
          <NavLink icon={<ShieldAlert size={16} />} href="/moderation" route={route} navigate={navigate}>{t("layout.moderation")}</NavLink>
          {canReviewVerification && (
            <NavLink icon={<BadgeCheck size={16} />} href="/verification" route={route} navigate={navigate}>{t("layout.verification")}</NavLink>
          )}
          {canReviewBotVerification && (
            <NavLink icon={<Stamp size={16} />} href="/bot-verification" route={route} navigate={navigate}>{t("layout.botVerification")}</NavLink>
          )}
          <NavLink icon={<AtSign size={16} />} href="/collectible-usernames" route={route} navigate={navigate}>{t("layout.collectibleUsernames")}</NavLink>
          <NavLink icon={<Phone size={16} />} href="/collectible-phones" route={route} navigate={navigate}>{t("layout.collectiblePhones")}</NavLink>
          <NavLink icon={<Trophy size={16} />} href="/account-ratings" route={route} navigate={navigate}>{t("layout.accountRatings")}</NavLink>
          <NavLink icon={<Rss size={16} />} href="/auto-subscribe-channels" route={route} navigate={navigate}>{t("layout.autoSubscribeChannels")}</NavLink>
		  <NavLink icon={<Database size={16} />} href="/storage" route={route} navigate={navigate}>{t("layout.storage")}</NavLink>
			<NavLink icon={<Gift size={16} />} href="/gifts" route={route} navigate={navigate}>{t("layout.gifts")}</NavLink>
          <NavLink icon={<Send size={16} />} href="/give-gifts" route={route} navigate={navigate}>{t("layout.giveGifts")}</NavLink>
          <NavLink icon={<Gavel size={16} />} href="/auctions" route={route} navigate={navigate}>{t("layout.auctions")}</NavLink>
          <NavLink icon={<Sticker size={16} />} href="/stickers" route={route} navigate={navigate}>{t("layout.stickers")}</NavLink>
          <NavLink icon={<Smile size={16} />} href="/emoji" route={route} navigate={navigate}>{t("layout.emoji")}</NavLink>
		  <NavLink icon={<Film size={16} />} href="/gif-catalog" route={route} navigate={navigate}>{t("layout.gifCatalog")}</NavLink>
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
            <ThemeSwitch />
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
