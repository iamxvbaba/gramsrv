import { useI18n } from "../i18n";
import { sectionById, visibleTabs, type NavTab } from "../nav";
import { usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import { AppLink } from "./AppLink";

// SectionTabs renders the in-page sub-menu shared by the consolidated sidebar
// entries (Accounts, Commerce, Verification, Gifts, Media, NFT Items): the
// sidebar link is flat, and the tabs inside the page switch between the sibling
// routes. Same visual language as Server Settings' Settings/Services bar.
//
// The tab list comes from the nav table, so it is permission-filtered by the
// same `can` the sidebar used: an operator who holds only prices.manage sees
// the Prices tab in the Commerce bar and nothing else. A section reduced to one
// visible tab draws no bar at all rather than a single lonely button.
export type SectionTab = { path: string; labelKey: string };

export function SectionTabs({
  sectionId,
  active,
  navigate
}: {
  sectionId: string;
  active: string;
  navigate: Navigate;
}) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const section = sectionById(sectionId);
  if (!section) {
    return null;
  }
  const tabs: NavTab[] = visibleTabs(section, can);
  if (tabs.length < 2) {
    return null;
  }
  return (
    <div className="tab-bar" role="tablist" aria-label={t("sectionTabs.label")}>
      {tabs.map((tab) => (
        <AppLink
          key={tab.path}
          href={tab.path}
          navigate={navigate}
          className={`tab-btn ${isActive(tab, active) ? "active" : ""}`}
        >
          {t(tab.labelKey)}
        </AppLink>
      ))}
    </div>
  );
}

// isActive lets a tab claim an alias path, so /messages (which renders the
// private view) marks the Private tab active instead of lighting none of them.
function isActive(tab: NavTab, active: string): boolean {
  return active === tab.path || (tab.aliases ?? []).includes(active);
}