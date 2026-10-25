import { ChevronsLeft, ChevronsRight, LogOut, UserRound } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api";
import { LanguageSwitch, useI18n } from "../i18n";
import { navSections, sectionActive, visibleTabs } from "../nav";
import { usePermissions } from "../permissions";
import { type Navigate, type RouteState, routeSubtitle, routeTitle } from "../routing";
import { ThemeSwitch } from "../theme";
import { AppLink } from "./AppLink";
import { AppBackground } from "./AppBackground";
import { withBase } from "../lib/base";

export function BootScreen() {
  const { t } = useI18n();
  return (
    <div className="boot-screen">
      <div className="brand compact brand-elevated">
        <span className="brand-mark"><img src={withBase("/logo.png")} alt="" /></span>
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
  const { can } = usePermissions();

  // Server identity (name/icon) is admin-editable per Server Settings ->
  // Server identity, and takes over the sidebar branding when set -- the
  // operator's own server should look like their server, not like the
  // "telesrv" reference build, once they've bothered to configure it.
  // Only fetched for sessions that can even see Server Settings; a session
  // without that permission just gets the default branding.
  const [identity, setIdentity] = useState<{ name: string; iconExt?: string } | null>(null);
  useEffect(() => {
    if (!can("server.manage")) return;
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
  }, [can]);
  const [brandIconFailed, setBrandIconFailed] = useState(false);
  const brandName = identity?.name?.trim() || "telesrv";
  const brandIconSrc = identity?.iconExt && !brandIconFailed ? api.serverIconURL() : withBase("/logo.png");

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
    const src = identity?.iconExt && !brandIconFailed ? api.serverIconURL() : withBase("/logo.png");
    // Browsers render the favicon file as-is -- they don't apply the sidebar's
    // border-radius to it -- so a square upload used to show square in the tab
    // strip while the sidebar showed the same file as a circle. Bake the same
    // rounded-square mask into the actual pixels so the tab matches the app.
    let cancelled = false;
    const img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = () => {
      if (cancelled) return;
      const size = 64;
      // 30% matches --radius of the .brand-mark in 01-foundation.css, so the
      // tab strip and the sidebar show the same silhouette.
      const radius = size * 0.3;
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
      // roundRect is not in every browser this panel supports, so the path is
      // built from four arcs and two lines by hand.
      ctx.moveTo(radius, 0);
      ctx.lineTo(size - radius, 0);
      ctx.arcTo(size, 0, size, radius, radius);
      ctx.lineTo(size, size - radius);
      ctx.arcTo(size, size, size - radius, size, radius);
      ctx.lineTo(radius, size);
      ctx.arcTo(0, size, 0, size - radius, radius);
      ctx.lineTo(0, radius);
      ctx.arcTo(0, 0, radius, 0, radius);
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
          {navSections.map((section) => {
            const tabs = visibleTabs(section, can);
            // A section the session holds none of the rights for is not shown:
            // every one of its routes is gated, so the row could only lead to a
            // denial screen. A section with exactly one visible tab still gets a
            // row -- that is the section's whole reason to exist.
            if (tabs.length === 0) {
              return null;
            }
            const Icon = section.icon;
            return (
              <NavLink
                key={section.id}
                icon={<Icon size={16} />}
                href={tabs[0].path}
                navigate={navigate}
                active={sectionActive(section, route.path, can)}
              >
                {t(section.labelKey)}
              </NavLink>
            );
          })}
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
  navigate,
  icon,
  children,
  active
}: {
  href: string;
  navigate: Navigate;
  icon?: ReactNode;
  children: ReactNode;
  // Computed by the caller from the nav table, which knows every sibling route
  // of the section. A section's row lights up on its detail pages too, so the
  // operator keeps their bearings inside /accounts/42.
  active: boolean;
}) {
  return (
    <AppLink className={`nav-item ${active ? "active" : ""}`} href={href} navigate={navigate}>
      {icon}
      <span>{children}</span>
    </AppLink>
  );
}
