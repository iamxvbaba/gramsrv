import { useI18n } from "../i18n";
import { matchTab, sectionById, visibleTabs, type NavTab } from "../nav";
import { usePermissions } from "../permissions";
import type { Navigate } from "../routing";
import { AppLink } from "./AppLink";

// SectionTabs renders the in-page sub-menu shared by the consolidated sidebar
// entries (Users and chats, Moderation, Verification, Content, Economy, Gifts,
// NFT items, Server): the sidebar link is flat, and the tabs inside the page
// switch between the sibling routes. Same visual language as Server Settings'
// Settings/Services bar.
//
// The tab list comes from the nav table, so it is permission-filtered by the
// same `can` the sidebar used: an operator who holds only prices.manage sees
// the Bot prices tab in the Economy bar and nothing else. A section reduced to
// one visible tab draws no bar at all rather than a single lonely button.
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
  // The tab that owns the route is lit on its detail pages too (/accounts/42,
  // /messages/groups/detail), and /messages lights the private tab through its
  // alias.
  const current = matchTab(tabs, active);
  return (
    <div className="tab-bar" role="tablist" aria-label={t("sectionTabs.label")}>
      {tabs.map((tab) => (
        <AppLink
          key={tab.path}
          href={tab.path}
          navigate={navigate}
          className={`tab-btn ${tab === current ? "active" : ""}`}
        >
          {t(tab.labelKey)}
        </AppLink>
      ))}
    </div>
  );
}
