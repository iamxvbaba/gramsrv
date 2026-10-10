import {
  AtSign,
  BadgeCheck,
  BarChart3,
  Bot,
  ChevronsLeft,
  ChevronsRight,
  Database,
  LayoutDashboard,
  LogOut,
  MessageSquareText,
  Megaphone,
  BadgeDollarSign,
  Star,
  ShieldAlert,
  ShieldCheck,
  Trophy,
  Users,
  UserCog,
  UserRound,
  Gift,
  ScrollText,
  Settings,
  Smile
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api";
import { LanguageSwitch, useI18n } from "../i18n";
import { permissionAuditRead, permissionAdminsManage, permissionBotVerificationReview, permissionDashboardRead, permissionMessagesRead, permissionPremiumManage, permissionServerManage, permissionStarsRead, permissionVerificationReview, useCan } from "../permissions";
import { type Navigate, type RouteState, routeSubtitle, routeTitle } from "../routing";
import { ThemeSwitch } from "../theme";
import { AppLink } from "./AppLink";
import { AppBackground } from "./AppBackground";

export function BootScreen() {
  const { t } = useI18n();
  return (
    <div className="boot-screen">
      <div className="brand compact brand-elevated">
        <span className="brand-mark"><img src="/logo.png" alt="" /></span>
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
  // The console's own administration and its action trail are granted separately
  // from every working section: managing operators (admins.manage) or reading
  // what everyone has done (audit.read) deserves an explicit decision, so the
  // entries below are not even visible without the matching right.
  const canManageAdmins = useCan(permissionAdminsManage);
  const canReadDashboard = useCan(permissionDashboardRead);
  const canReadAudit = useCan(permissionAuditRead);
  const canManageServer = useCan(permissionServerManage);
  const canReadStars = useCan(permissionStarsRead);
  const canReadMessages = useCan(permissionMessagesRead);

  // Server identity (name/icon) is admin-editable per Server Settings ->
  // Server identity, and takes over the sidebar branding when set -- the
  // operator's own server should look like their server, not like the
  // "telesrv" reference build, once they've bothered to configure it.
  // Only fetched for sessions that can even see Server Settings; a session
  // without that permission just gets the default branding.
  const [identity, setIdentity] = useState<{ name: string; iconExt?: string } | null>(null);
  useEffect(() => {
    if (!canManageServer) return;
    const load = () => {
      api.serverIdentity()
        .then((info) => setIdentity({ name: info.name, iconExt: info.icon_ext }))
        .catch(() => undefined);
    };
    load();
    // Server Settings fires this after saving a name/description/icon so the
    // sidebar and tab rebrand without a full page reload.
    window.addEventListener("telesrv:identity-changed", load);
    return () => window.removeEventListener("telesrv:identity-changed", load);
  }, [canManageServer]);
  const [brandIconFailed, setBrandIconFailed] = useState(false);
  const brandName = identity?.name?.trim() || "telesrv";
  const brandIconSrc = identity?.iconExt && !brandIconFailed ? api.serverIconURL() : "/logo.png";

  // The browser tab (title + favicon) follows the same custom-identity
  // override as the sidebar brand above, so a re-labeled server actually
  // looks like itself in the tab strip too, not just inside the app.
  useEffect(() => {
    document.title = `${brandName} admin`;
  }, [brandName]);
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    const linkEl = link;
    const src = identity?.iconExt && !brandIconFailed ? api.serverIconURL() : "/logo.png";
    // Browsers render the favicon file as-is -- they don't apply the
    // sidebar's CSS border-radius to it, so a square-cornered source image
    // shows up square in the tab strip. Bake the circular mask into the
    // actual pixels instead, the same way an app icon export would.
    let cancelled = false;
    const img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = () => {
      if (cancelled) return;
      const size = 64;
      const canvas = document.createElement("canvas");
      canvas.width = size;
      canvas.height = size;
      const ctx = canvas.getContext("2d");
      if (!ctx) {
        linkEl.href = src;
        return;
      }
      ctx.save();
      ctx.beginPath();
      ctx.arc(size / 2, size / 2, size / 2, 0, Math.PI * 2);
      ctx.closePath();
      ctx.clip();
      ctx.drawImage(img, 0, 0, size, size);
      ctx.restore();
      linkEl.href = canvas.toDataURL("image/png");
    };
    img.onerror = () => {
      if (!cancelled) {
        linkEl.href = src;
      }
    };
    img.src = src;
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity?.iconExt, brandIconFailed]);

  async function logout() {
    await api.logout().catch(() => undefined);
    onLogout();
  }

  // Mobile keeps the sidebar as an icon rail; the button at the bottom of the
  // rail expands it into the full labeled sidebar. On desktop the toggle is
  // hidden entirely and the sidebar is always fully expanded.
  const [sidebarExpanded, setSidebarExpanded] = useState(false);

  return (
    <div className={`shell${sidebarExpanded ? " sidebar-expanded" : ""}`}>
      <aside className="sidebar">
        <AppLink className="brand" href="/" navigate={navigate}>
          <span className="brand-mark"><img src={brandIconSrc} alt={brandName} onError={() => setBrandIconFailed(true)} /></span>
          <span>
            <strong>{brandName}</strong>
            <small>{t("app.adminConsole")}</small>
          </span>
        </AppLink>
        <div className="sidebar-label">{t("layout.navigation")}</div>
        <nav className="nav-list" aria-label={t("layout.primaryNav")}>
          <NavLink icon={<LayoutDashboard size={16} />} href="/" route={route} navigate={navigate}>{t("layout.dashboard")}</NavLink>
          {canReadDashboard && (
            <NavLink icon={<BarChart3 size={16} />} href="/statistics" route={route} navigate={navigate}>{t("layout.statistics")}</NavLink>
          )}
          <NavLink icon={<Users size={16} />} href="/accounts" route={route} navigate={navigate}>{t("layout.accounts")}</NavLink>
          <NavLink icon={<Trophy size={16} />} href="/account-ratings" route={route} navigate={navigate}>{t("layout.accountRatings")}</NavLink>
          <NavLink icon={<ShieldCheck size={16} />} href="/channels" route={route} navigate={navigate}>{t("layout.channels")}</NavLink>
          <NavLink icon={<Bot size={16} />} href="/bots" route={route} navigate={navigate}>{t("layout.bots")}</NavLink>
          {canReadStars && (
            <NavLink icon={<Star size={16} />} href="/stars" route={route} navigate={navigate}>{t("layout.stars")}</NavLink>
          )}
          <NavLink
            icon={<Gift size={16} />}
            href="/gifts"
            route={route}
            navigate={navigate}
            activeWhen={(path) => path.startsWith("/gifts") || path.startsWith("/auctions")}
          >
            {t("layout.gifts")}
          </NavLink>
          <NavLink
            icon={<BadgeDollarSign size={16} />}
            href={canManagePremium ? "/monetization" : "/give-gifts"}
            route={route}
            navigate={navigate}
            activeWhen={(path) => path.startsWith("/monetization") || path.startsWith("/premium") || path.startsWith("/give-gifts") || path.startsWith("/star-issue")}
          >
            {t("layout.grants")}
          </NavLink>
          <NavLink
            icon={<AtSign size={16} />}
            href="/collectible-usernames"
            route={route}
            navigate={navigate}
            activeWhen={(path) => path.startsWith("/collectible-usernames") || path.startsWith("/collectible-phones") || path.startsWith("/nft-gifts")}
          >
            {t("layout.nftItems")}
          </NavLink>
          {(canReviewVerification || canReviewBotVerification) && (
            <NavLink
              icon={<BadgeCheck size={16} />}
              href={canReviewVerification ? "/verification" : "/bot-verification"}
              route={route}
              navigate={navigate}
              activeWhen={(path) => path.startsWith("/verification") || path.startsWith("/bot-verification")}
            >
              {t("layout.verification")}
            </NavLink>
          )}
          <NavLink
            icon={<Smile size={16} />}
            href="/stickers"
            route={route}
            navigate={navigate}
            activeWhen={(path) => path.startsWith("/stickers") || path.startsWith("/emoji") || path.startsWith("/gif-catalog")}
          >
            {t("layout.media")}
          </NavLink>
          <NavLink icon={<Database size={16} />} href="/storage" route={route} navigate={navigate}>{t("layout.storage")}</NavLink>
          <NavLink icon={<Megaphone size={16} />} href="/broadcasts" route={route} navigate={navigate}>{t("layout.broadcasts")}</NavLink>
          {canReadMessages && (
            <NavLink
              icon={<MessageSquareText size={16} />}
              href="/messages/private"
              route={route}
              navigate={navigate}
              activeWhen={(path) => path.startsWith("/messages")}
            >
              {t("layout.messages")}
            </NavLink>
          )}
          <NavLink icon={<ShieldAlert size={16} />} href="/moderation" route={route} navigate={navigate}>{t("layout.moderation")}</NavLink>
          {canReadAudit && (
            <NavLink icon={<ScrollText size={16} />} href="/audit-log" route={route} navigate={navigate}>{t("layout.auditLog")}</NavLink>
          )}
          {canManageAdmins && (
            <NavLink icon={<UserCog size={16} />} href="/admin-users" route={route} navigate={navigate}>{t("layout.adminUsers")}</NavLink>
          )}

          {canManageServer && (
            <NavLink icon={<Settings size={16} />} href="/server-settings" route={route} navigate={navigate}>{t("layout.serverSettings")}</NavLink>
          )}
        </nav>
        <div className="sidebar-toggle-wrap">
          <button
            className="sidebar-toggle"
            type="button"
            aria-expanded={sidebarExpanded}
            aria-label={sidebarExpanded ? t("layout.collapseSidebar") : t("layout.expandSidebar")}
            title={sidebarExpanded ? t("layout.collapseSidebar") : t("layout.expandSidebar")}
            onClick={() => setSidebarExpanded((open) => !open)}
          >
            {sidebarExpanded ? <ChevronsLeft size={16} /> : <ChevronsRight size={16} />}
            <span>{sidebarExpanded ? t("layout.collapseSidebar") : t("layout.expandSidebar")}</span>
          </button>
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
            <span className="actor-pill"><UserRound size={14} /> {actor}</span>
            <button className="btn ghost icon-text" type="button" onClick={logout} title={t("layout.logout")}>
              <LogOut size={16} /> {t("layout.logout")}
            </button>
          </div>
        </header>
        <main className="content">{children}</main>
      </div>
      <AppBackground className="app-background--workspace" />
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
