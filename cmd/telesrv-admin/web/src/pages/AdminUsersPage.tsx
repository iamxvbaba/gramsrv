import { KeyRound, Lock, RefreshCw, ShieldCheck, UserPlus, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { ActionButton } from "../components/ActionButton";
import { Alert, Badge, EmptyRow, PageFrame, QueryPanel, SectionHead } from "../components/ui";
import { useI18n } from "../i18n";
import { groupPermissions, permissionHint, permissionTitle } from "../permissions";
import { formatDate } from "../lib/format";
import type { AdminConsoleUser, AdminConsoleSystemOperator } from "../types";

// The operator-accounts screen. The table only reports; every change happens in
// a modal and goes through the panel's usual reason + dry-run + confirm flow,
// because handing somebody the run of the console deserves the same "here is
// what this will do" step as freezing an account.
//
// Everything here is additionally enforced server-side by admins.manage --
// hiding the section is a convenience, not the boundary.
export function AdminUsersPage() {
  const { t } = useI18n();
  const [rows, setRows] = useState<AdminConsoleUser[]>([]);
  const [system, setSystem] = useState<AdminConsoleSystemOperator | null>(null);
  const [available, setAvailable] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<AdminConsoleUser | null>(null);
  const [resetting, setResetting] = useState<AdminConsoleUser | null>(null);
  const [creating, setCreating] = useState(false);

  async function load() {
    setBusy(true);
    setError("");
    try {
      const result = await api.adminUsers();
      setRows(result.rows ?? []);
      setSystem(result.system ?? null);
      setAvailable(result.available_permissions ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setLoaded(true);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  return (
    <PageFrame eyebrow={t("operators.eyebrow")} title={t("operators.title")}>
      {error && <Alert>{error}</Alert>}

      <QueryPanel>
        <div className="toolbar">
          <button className="btn primary icon-text" type="button" onClick={() => setCreating(true)}>
            <UserPlus size={15} /> {t("operators.new")}
          </button>
          <button className="btn icon-text" type="button" onClick={() => void load()} disabled={busy}>
            <RefreshCw size={15} className={busy ? "spin" : ""} /> {t("operators.refresh")}
          </button>
        </div>
      </QueryPanel>

      <SectionHead title={t("operators.heading")} />
      <div className="table-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t("operators.username")}</th>
              <th>{t("operators.canDo")}</th>
              <th>{t("operators.status")}</th>
              <th>{t("operators.lastLogin")}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {/* The built-in operator first: it has the most rights and no
                database row, so a list that started with the named accounts
                would put the most powerful login last, or nowhere. */}
            {system && (
              <tr>
                <td className="mono">
                  {system.username} <Badge>{t("operators.builtIn")}</Badge>
                </td>
                <td><PermissionChips permissions={system.permissions} /></td>
                <td><Badge tone="good">{t("operators.enabled")}</Badge></td>
                <td className="mono">{"—"}</td>
                <td>
                  <span className="muted icon-text">
                    <Lock size={13} /> {t("operators.setInEnvironment")}
                  </span>
                </td>
              </tr>
            )}
            {rows.map((row) => (
              <tr key={row.id}>
                <td className="mono">{row.username}</td>
                <td><PermissionChips permissions={row.permissions} /></td>
                <td>
                  {row.enabled
                    ? <Badge tone="good">{t("operators.enabled")}</Badge>
                    : <Badge tone="danger">{t("operators.disabled")}</Badge>}
                </td>
                <td className="mono">{row.last_login_at ? formatDate(row.last_login_at) : "—"}</td>
                <td>
                  <span className="row-actions">
                    <button className="btn icon-text" type="button" onClick={() => setEditing(row)}>
                      <ShieldCheck size={14} /> {t("operators.access")}
                    </button>
                    <button className="btn icon-text" type="button" onClick={() => setResetting(row)}>
                      <KeyRound size={14} /> {t("operators.password")}
                    </button>
                  </span>
                </td>
              </tr>
            ))}
            {(!sysRows(system, rows, loaded, busy)) && <EmptyRow colSpan={5} />}
          </tbody>
        </table>
      </div>

      {creating && (
        <OperatorModal
          title={t("operators.createTitle")}
          available={available}
          onClose={() => setCreating(false)}
          onDone={() => { setCreating(false); void load(); }}
        />
      )}
      {editing && (
        <OperatorModal
          title={t("operators.accessFor", { username: editing.username })}
          available={available}
          existing={editing}
          onClose={() => setEditing(null)}
          onDone={() => { setEditing(null); void load(); }}
        />
      )}
      {resetting && (
        <PasswordModal
          operator={resetting}
          onClose={() => setResetting(null)}
          onDone={() => { setResetting(null); void load(); }}
        />
      )}
    </PageFrame>
  );
}

function sysRows(
  system: AdminConsoleSystemOperator | null,
  rows: AdminConsoleUser[],
  loaded: boolean,
  busy: boolean
): boolean {
  if (busy && !loaded) {
    return true;
  }
  return Boolean(system) || rows.length > 0;
}

function PermissionChips({ permissions }: { permissions: string[] }) {
  const { t } = useI18n();
  if (permissions.length === 0) {
    return <span className="muted">{t("operators.nothingYet")}</span>;
  }
  return (
    <span className="chip-row">
      {permissions.map((p) => (
        <span className="chip" key={p} title={p}>{permissionTitle(p)}</span>
      ))}
    </span>
  );
}

// PermissionPicker lists the rights by what they let someone do, split into the
// section of the console each governs -- twenty-six checkboxes in one run is a
// wall nobody reads, and the grouping is what makes "what can this person
// actually touch" answerable at a glance.
//
// The raw permission string stays as each row's tooltip, so the screen never
// hides what is actually being stored.
//
// The wildcard has deliberately no box here: the server refuses to grant "*"
// to a named account (security.go validatePermissions), so the editor offers
// the assignable list and nothing else. An account that predates this and
// still holds "*" renders as its chips in the table and keeps the marker until
// an operator edits it into an explicit list -- which is exactly the
// migration path. The chips are read-only display; Has() answering true for
// everything is how "*" has always behaved.
function PermissionPicker({
  available,
  selected,
  onToggle,
  onToggleGroup
}: {
  available: string[];
  selected: string[];
  onToggle: (permission: string, on: boolean) => void;
  onToggleGroup: (permissions: string[], on: boolean) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="permission-groups">
      {groupPermissions(available).map((group) => {
        const all = group.permissions.length > 0 && group.permissions.every((p) => selected.includes(p));
        return (
          <section className="permission-group" key={group.title}>
            <div className="permission-group-head">
              <div>
                <strong>{group.title}</strong>
                <small>{group.hint}</small>
              </div>
              <button
                className="btn compact-btn"
                type="button"
                onClick={() => onToggleGroup(group.permissions, !all)}
              >
                {all ? t("operators.clear") : t("operators.selectAll")}
              </button>
            </div>
            <div className="permission-grid">
              {group.permissions.map((permission) => (
                <label className="permission-item" key={permission} title={permission}>
                  <input
                    type="checkbox"
                    checked={selected.includes(permission)}
                    onChange={(event) => onToggle(permission, event.target.checked)}
                  />
                  <span className="permission-copy">
                    <strong>{permissionTitle(permission)}</strong>
                    <small>{permissionHint(permission)}</small>
                  </span>
                </label>
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

// OperatorModal creates a new operator, or edits an existing one's access. The
// same shape either way: the only difference is whether a username and password
// are being chosen.
//
// Laid out as head / scrolling body / action bar like every other command modal
// in the panel, so a long permission list scrolls inside the dialog instead of
// pushing its own confirm button off the screen.
function OperatorModal({
  title,
  available,
  existing,
  onClose,
  onDone
}: {
  title: string;
  available: string[];
  existing?: AdminConsoleUser;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [username, setUsername] = useState(existing?.username ?? "");
  const [password, setPassword] = useState("");
  const [permissions, setPermissions] = useState<string[]>(existing?.permissions ?? []);
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const isEdit = Boolean(existing);

  // Only the shape the server insists on: a username it will accept, and a
  // password that is actually present. Length is the operator's business.
  const incomplete = isEdit
    ? false
    : username.trim().length < 3 || password.trim() === "";

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={title}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("operators.eyebrow")}</div>
            <h2>{title}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label={t("action.close")}><X size={15} /></button>
        </div>

        <div className="command-body">
          {!isEdit && (
            <div className="operator-identity">
              <label className="form-field">
                <span>{t("operators.usernameField")}</span>
                <input
                  autoFocus
                  value={username}
                  spellCheck={false}
                  autoCapitalize="none"
                  placeholder={t("operators.usernamePlaceholder")}
                  onChange={(event) => setUsername(event.target.value)}
                />
              </label>
              <label className="form-field">
                <span>{t("operators.passwordField")}</span>
                <input
                  type="password"
                  value={password}
                  autoComplete="new-password"
                  onChange={(event) => setPassword(event.target.value)}
                />
              </label>
            </div>
          )}

          <PermissionPicker
            available={available}
            selected={permissions}
            onToggle={(permission, on) =>
              setPermissions((current) =>
                on ? [...current, permission] : current.filter((p) => p !== permission)
              )
            }
            onToggleGroup={(group, on) =>
              setPermissions((current) =>
                on
                  ? [...current, ...group.filter((p) => !current.includes(p))]
                  : current.filter((p) => !group.includes(p))
              )
            }
          />

          <label className="permission-item standalone">
            <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
            <span className="permission-copy">
              <strong>{t("operators.accountEnabled")}</strong>
              <small>{t("operators.accountEnabledHint")}</small>
            </span>
          </label>

          {isEdit && (
            <Alert>{t("operators.appliesNextRequest")}</Alert>
          )}
        </div>

        <div className="modal-actions toolbar">
          <button className="btn" type="button" onClick={onClose}>{t("operators.cancel")}</button>
          <ActionButton
            label={isEdit ? t("operators.saveAccess") : t("operators.createOperator")}
            path={isEdit ? "/api/actions/set-admin-operator-access" : "/api/actions/create-admin-operator"}
            tone="primary"
            disabled={incomplete}
            icon={isEdit ? <ShieldCheck size={15} /> : <UserPlus size={15} />}
            payload={() =>
              isEdit
                ? { id: existing?.id, permissions, enabled }
                : { username: username.trim(), password, permissions, enabled }
            }
            secretField={isEdit ? undefined : "password"}
            onDone={onDone}
          />
        </div>
      </section>
    </div>,
    document.body
  );
}

function PasswordModal({
  operator,
  onClose,
  onDone
}: {
  operator: AdminConsoleUser;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal narrow" role="dialog" aria-modal="true" aria-label={t("operators.setPassword")}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{t("operators.eyebrow")}</div>
            <h2>{t("operators.passwordFor", { username: operator.username })}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label={t("action.close")}><X size={15} /></button>
        </div>

        <div className="command-body">
          <label className="form-field">
            <span>{t("operators.newPassword")}</span>
            <input
              autoFocus
              type="password"
              value={password}
              autoComplete="new-password"
              onChange={(event) => setPassword(event.target.value)}
            />
          </label>
          <Alert>{t("operators.passwordBumps")}</Alert>
        </div>

        <div className="modal-actions toolbar">
          <button className="btn" type="button" onClick={onClose}>{t("operators.cancel")}</button>
          <ActionButton
            label={t("operators.setPassword")}
            path="/api/actions/set-admin-operator-password"
            tone="primary"
            disabled={password.trim() === ""}
            icon={<KeyRound size={15} />}
            payload={() => ({ id: operator.id, password })}
            secretField="password"
            onDone={onDone}
          />
        </div>
      </section>
    </div>,
    document.body
  );
}