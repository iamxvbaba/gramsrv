import { ShieldOff } from "lucide-react";
import { createContext, useContext, useMemo, type ReactNode } from "react";
import { Alert, PageFrame } from "./components/ui";
import { useI18n } from "./i18n";

// Permission names exactly as the backend spells them
// (cmd/telesrv-admin/security.go). "*" is the wildcard an operator configures for
// a full-access session.
export const permissionAll = "*";
export const permissionVerificationReview = "verification.review";
export const permissionVerificationRevoke = "verification.revoke";

// GET /api/session is read once at boot; the panel keeps the answer here so a
// section the session may not use is hidden instead of rendered into a 403. This
// is a convenience for the operator, not a security boundary: every route is
// checked again server-side.
const PermissionsContext = createContext<readonly string[]>([]);

export function PermissionsProvider({
  permissions,
  children
}: {
  permissions: readonly string[];
  children: ReactNode;
}) {
  return <PermissionsContext.Provider value={permissions}>{children}</PermissionsContext.Provider>;
}

export function usePermissions(): { permissions: readonly string[]; can: (permission: string) => boolean } {
  const permissions = useContext(PermissionsContext);
  return useMemo(
    () => ({
      permissions,
      can: (permission: string) => permissions.includes(permissionAll) || permissions.includes(permission)
    }),
    [permissions]
  );
}

export function useCan(permission: string): boolean {
  return usePermissions().can(permission);
}

// PermissionGate is what a direct URL hits: without the right the operator gets
// an explanation naming the missing permission, not an empty table that looks
// like "no data".
export function PermissionGate({ permission, children }: { permission: string; children: ReactNode }) {
  const { can } = usePermissions();
  if (can(permission)) {
    return <>{children}</>;
  }
  return <PermissionDenied permission={permission} />;
}

export function PermissionDenied({ permission }: { permission: string }) {
  const { t } = useI18n();
  return (
    <PageFrame title={t("permission.deniedTitle")} eyebrow={t("permission.deniedEyebrow")}>
      <Alert>{t("permission.deniedBody", { permission })}</Alert>
      <section className="section-block">
        <div className="entity-head">
          <div>
            <div className="entity-title"><ShieldOff size={16} /> {t("permission.deniedHeading")}</div>
            <div className="entity-subtitle">{t("permission.deniedHint")}</div>
          </div>
        </div>
      </section>
    </PageFrame>
  );
}
