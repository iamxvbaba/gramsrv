"""Generate the admin-panel API doc's route and permission tables from source.

The reason this script exists: those tables drifted ~40% out of date without
anyone noticing, because nothing checked them. They are generated from
web/src/nav.ts, web/src/permissions.tsx and cmd/telesrv-admin/server.go
instead, and the doc carries the generated marker so a hand edit is visible in
review as a diff against the next regeneration.

Usage:
    python3 scripts/gen_admin_panel_doc_tables.py            # print
    python3 scripts/gen_admin_panel_doc_tables.py --check     # exit 1 if stale
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
WEB = ROOT / "cmd/telesrv-admin/web/src"
WEB_PAGES = WEB / "pages"
SERVER_GO = ROOT / "cmd/telesrv-admin/server.go"

# ---------------------------------------------------------------- nav sections

# The section header is matched on its own and the tabs array is then read out by
# bracket matching. It used to be captured by one non-greedy (?P<tabs>.*?)
# terminated with `\]\s*\}`, which stops at the first `]` that happens to be
# followed by a `}` -- i.e. the closing bracket of an entry's own
# `aliases: [...]` -- so a section whose tabs carry aliases lost its tabs and
# dropped out of the table with no error at all. /messages/private is that
# entry, and the docs silently lost the Messages section because of it.
SECTION_RE = re.compile(
    r'\{\s*(?://[^\n]*\n\s*)*id:\s*"(?P<id>[a-z0-9-]+)",\s*labelKey:\s*"(?P<label>[^"]+)",'
    r'\s*icon:\s*\w+,\s*tabs:\s*\['
)
TAB_RE = re.compile(
    r'\{\s*path:\s*"(?P<path>[^"]+)",\s*labelKey:\s*"(?P<label>[a-zA-Z.]+)",'
    r'\s*permission:\s*(?P<perm>\w+)(?P<rest>[^}]*)\}'
)

ALIAS_RE = re.compile(r'aliases:\s*\[(?P<paths>[^\]]*)\]')


def parse_aliases(rest: str) -> list[str]:
    """Return the extra paths a tab entry serves, if it lists any.

    A tab that carries aliases is served under a second URL as well: the
    Messages tab answers to a bare /messages, and the Premium tab still
    answers to the old /star-issue. The table prints those paths, so
    the URL a bookmark points at is written down next to the one the tab
    lives at.
    """
    match = ALIAS_RE.search(rest)
    if not match:
        return []
    return re.findall(r'"([^"]+)"', match.group("paths"))


def read_bracket_array(src: str, open_bracket: int) -> str:
    """Return the text between the `[` at open_bracket and its matching `]`."""
    depth = 0
    for i in range(open_bracket, len(src)):
        if src[i] == "[":
            depth += 1
        elif src[i] == "]":
            depth -= 1
            if depth == 0:
                return src[open_bracket + 1 : i]
    raise ValueError(f"unbalanced [ at offset {open_bracket} of nav.ts")


def parse_nav() -> list[tuple[str, str, list[tuple[str, str, str, list[str]]]]]:
    """Return [(section id, label key, [(path, label key, permission const, aliases)])]."""
    src = (WEB / "nav.ts").read_text(encoding="utf-8")
    body = src[src.index("export const navSections"):]
    body = body[: body.index("\n];")]

    out = []
    pos = 0
    while (m := SECTION_RE.search(body, pos)) is not None:
        tabs_src = read_bracket_array(body, m.end() - 1)
        pos = m.end() + len(tabs_src) + 1
        tabs = []
        for t in TAB_RE.finditer(tabs_src):
            tabs.append(
                (
                    t.group("path"),
                    t.group("label"),
                    t.group("perm"),
                    parse_aliases(t.group("rest")),
                )
            )
        # A section that parses to zero tabs is a broken parse, not an empty
        # section -- nav.ts has no such thing, and quietly dropping one is
        # exactly how these tables rotted in the first place.
        if not tabs:
            raise ValueError(f"nav section {m.group('id')!r} parsed to zero tabs")
        out.append((m.group("id"), m.group("label"), tabs))
    # Every section object in nav.ts starts with an `id:`; if the regex matched
    # fewer sections than there are ids, it silently skipped one (a comment or a
    # hyphen in the id, say), which is exactly how these tables rot. Fail loudly
    # instead of emitting docs that are missing a section.
    declared = len(re.findall(r'\bid:\s*"', body))
    if declared != len(out):
        raise ValueError(
            f"parsed {len(out)} nav sections but nav.ts declares {declared} 'id:' entries; "
            "SECTION_RE missed a section"
        )
    return out


# ----------------------------------------------------------------- permissions

PERM_CONST_RE = re.compile(r'export const (\w+) = "([a-zA-Z.*]+)";')
LABEL_RE = re.compile(r'"([a-zA-Z.*]+)":\s*\{\s*title:\s*"((?:[^"\\]|\\.)*)",\s*hint:\s*"((?:[^"\\]|\\.)*)"')
# The group array is one object per line-per-field, so match each object as a
# unit rather than relying on the fields being adjacent.
GROUP_TITLE_RE = re.compile(
    r'title:\s*"([^"]+)",\s*\n\s*hint:\s*"([^"]+)",\s*\n\s*permissions:\s*\[([^\]]*)\]',
)
GROUP_ITEM_RE = re.compile(r'"([a-zA-Z.*]+)"')


def parse_i18n(lang: str = "en") -> dict[str, str]:
    """English UI strings, so the tables show what an operator reads rather than
    an i18n key. Falls back to the key itself when a string is missing, which
    is itself a useful failure signal in review."""
    src = (WEB / "i18n.tsx").read_text(encoding="utf-8")
    body = src[src.index(f"  {lang}: {{") + len(lang) + 6:]
    nxt = [m.start() for m in re.finditer(r"^  (?:en|zh|ru): \{$", body, re.M)]
    body = body[: nxt[0]] if nxt else body
    out = {}
    for m in re.finditer(r'^\s*"([a-zA-Z0-9_.]+)":\s*"((?:[^"\\]|\\.)*)"', body, re.M):
        out[m.group(1)] = m.group(2)
    return out


def parse_permissions() -> tuple[dict[str, str], dict[str, str], list[tuple[str, str, list[str]]], list[str]]:
    """Return (const name -> name, name -> (title, hint), groups, assignable order)."""
    src = (WEB / "permissions.tsx").read_text(encoding="utf-8")
    const_to_name = dict(PERM_CONST_RE.findall(src))

    labels: dict[str, str] = {}
    label_block = src[src.index("const permissionLabels"): src.index("export function permissionTitle")]
    for m in LABEL_RE.finditer(label_block):
        labels[m.group(1)] = f"{m.group(2)} — {m.group(3)}"

    groups: list[tuple[str, str, list[str]]] = []
    group_src = src[src.index("export const permissionGroups"):]
    group_src = group_src[: group_src.index("\n];")]
    for m in GROUP_TITLE_RE.finditer(group_src):
        groups.append((m.group(1), m.group(2), GROUP_ITEM_RE.findall(m.group(3))))

    return const_to_name, labels, groups, sorted(labels)


SECURITY_GO = ROOT / "cmd/telesrv-admin/security.go"


def parse_assignable() -> list[str]:
    """Permission names in the order assignablePermissions() lists them.

    This is the list the operator-accounts screen offers, so it -- not
    permissionLabels -- is the set of rights that can actually be granted, and
    it is therefore the set the documentation is about.
    """
    src = SECURITY_GO.read_text(encoding="utf-8")
    block = src[src.index("func assignablePermissions() []string {"):]
    block = block[: block.index("\n}\n")]
    const_to_name = dict(PERM_CONST_RE.findall((WEB / "permissions.tsx").read_text(encoding="utf-8")))
    names = []
    for line in block.splitlines():
        m = re.match(r"\s*(permission\w+|permissionAll),", line)
        if m:
            names.append(const_to_name.get(m.group(1), m.group(1)))
    return names


# ------------------------------------------------------------- server routes

ROUTE_RE = re.compile(
    r'mux\.Handle(?:Func)?\(\s*"(?P<method>GET|POST|PUT|DELETE)\s+(?P<path>[^"]+)"\s*,\s*'
    r'(?P<gate>s\.\w+)\((?P<handler>[\w.]+)'
)


def parse_routes() -> list[tuple[str, str, str, str]]:
    """Return [(method, path, gate name, handler)] for the panel's own routes."""
    src = SERVER_GO.read_text(encoding="utf-8")
    body = src[src.index("func (s *server) routes() http.Handler {"):]
    body = body[: body.index("\n}\n")]
    out = []
    for m in ROUTE_RE.finditer(body):
        gate = m.group("gate")
        if gate == "mux.HandleFunc" or gate.endswith("HandleFunc"):
            continue
        gate = gate.rsplit(".", 1)[-1]
        out.append((m.group("method"), m.group("path"), gate, m.group("handler")))
    return out


# ------------------------------------------------------- section tab placement


def pages_rendering_section_tabs() -> list[str]:
    """Pages that render a <SectionTabs> of their own.

    The shell (components/Layout.tsx) draws the active section's tab bar above
    every page from nav.ts, so a page that also renders <SectionTabs> shows the
    bar twice. That compiles and renders either way, so it is checked here for
    the same reason the tables are: drift that renders is exactly the kind that
    survives review.
    """
    bad: list[str] = []
    for page in sorted(WEB_PAGES.rglob("*.tsx")):
        for lineno, line in enumerate(page.read_text(encoding="utf-8").splitlines(), 1):
            if "<SectionTabs" in line:
                bad.append(f"{page.relative_to(ROOT).as_posix()}:{lineno}")
    return bad


# ------------------------------------------------------------------- rendering

GATE_TO_PERMISSION = {
    "scopedRoute": None,  # resolved per-route below
    "serverManage": "server.manage",
    "premiumManage": "premium.manage",
    "requireAdminsManage": "admins.manage",
}


def gate_permission(gate: str) -> str:
    if gate in GATE_TO_PERMISSION and GATE_TO_PERMISSION[gate]:
        return GATE_TO_PERMISSION[gate]
    if gate == "scopedRoute":
        return "per-route"
    return gate


def render_tables() -> dict[str, str]:
    const_to_name, labels, _groups, all_names = parse_permissions()
    i18n = parse_i18n()
    assignable = parse_assignable()
    sections = parse_nav()
    routes = parse_routes()

    def label(key: str) -> str:
        # A missing string is a bug worth seeing in the diff, not hiding.
        return i18n.get(key, f"`{key}` (missing)")

    # -- sections / tabs -----------------------------------------------------
    # One row per tab. The section repeats rather than being left blank: a
    # generated table with merged cells renders unpredictably once it is
    # copy-pasted into anything else.
    sections_md = [
        "| Sidebar section | Tab | Path | Required right |",
        "| --- | --- | --- | --- |",
    ]
    tab_count = 0
    for _sid, section_key, tabs in sections:
        for path, tab_key, perm, aliases in tabs:
            name = const_to_name.get(perm, perm)
            suffix = ""
            if aliases:
                extra = ", ".join(f"`{alias}`" for alias in aliases)
                suffix = f" (also served at {extra})"
            sections_md.append(
                f"| {label(section_key)} | {label(tab_key)} | `{path}` | `{name}`{suffix} |"
            )
            tab_count += 1

    # -- permissions ---------------------------------------------------------
    # Grouped the way the operator-accounts screen groups them, so the doc and
    # the picker an operator actually clicks through read the same way.
    perms_md = ["| Permission | Grants |", "| --- | --- |"]
    seen: set[str] = set()
    _c2n, _lbl, groups, _all = parse_permissions()
    for title, _hint, members in groups:
        rows = []
        for name in members:
            if name not in assignable or name in seen:
                continue
            seen.add(name)
            rows.append(f"| `{name}` | {labels.get(name, '—')} |")
        if rows:
            perms_md.append(f"| **{title}** | |")
            perms_md.extend(rows)
    leftovers = [n for n in assignable if n not in seen]
    if leftovers:
        perms_md.append("| **Ungrouped** | |")
        perms_md.extend(f"| `{n}` | {labels.get(n, '—')} |" for n in leftovers)
    perms_md.append("| `*` | Every right, including ones added later (wildcard). |")

    return {
        "sections": "\n".join(sections_md),
        "permissions": "\n".join(perms_md),
        "assignable_count": str(len(assignable)),
        "permission_count": str(len(all_names)),
        "section_count": str(len(sections)),
        "tab_count": str(tab_count),
        "route_count": str(len(routes)),
        "unlabelled": ", ".join(n for n in assignable if n not in labels) or "none",
    }


BEGIN = "<!-- BEGIN GENERATED:{block} -->"
END = "<!-- END GENERATED:{block} -->"
NOTE = (
    "<!-- GENERATED by scripts/gen_admin_panel_doc_tables.py -- run it and paste"
    " the output, or use --check in CI. Edit nav.ts / permissions.tsx instead. -->"
)


def block(block_name: str, body: str) -> str:
    return f"{BEGIN.format(block=block_name)}\n{body}\n{END.format(block=block_name)}"


def splice(doc_path: pathlib.Path, block_name: str, body: str) -> bool:
    """Replace one generated block in a doc file. Returns True if it changed."""
    doc = doc_path.read_text(encoding="utf-8")
    start = doc.index(BEGIN.format(block=block_name))
    end = doc.index(END.format(block=block_name))
    updated = doc[:start] + block(block_name, body) + doc[end + len(END.format(block=block_name)) :]
    if updated == doc:
        return False
    doc_path.write_text(updated, encoding="utf-8")
    return True


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any generated block in the docs is stale",
    )
    parser.add_argument(
        "--write",
        action="store_true",
        help="rewrite the generated blocks in the docs in place",
    )
    args = parser.parse_args()

    tables = render_tables()
    blocks = {
        "nav": tables["sections"],
        "permissions": tables["permissions"],
    }
    docs = [
        ROOT / "docs/admin-panel-api.en.md",
        ROOT / "docs/admin-panel-api.ru.md",
    ]

    if args.check:
        stale = []
        for doc_path in docs:
            if not doc_path.exists():
                continue
            doc = doc_path.read_text(encoding="utf-8")
            for name, body in blocks.items():
                begin = BEGIN.format(block=name)
                end = END.format(block=name)
                if begin not in doc or end not in doc:
                    continue
                # Compare the body *between* the markers: including the BEGIN
                # line on one side and the END line on the other would report
                # every file as stale.
                current = doc[doc.index(begin) + len(begin) : doc.index(end)]
                if current.strip() != body.strip():
                    stale.append(f"{doc_path.name}: {name}")
        if stale:
            print("stale generated tables:\n  " + "\n  ".join(stale), file=sys.stderr)
            print("\nrun: python3 scripts/gen_admin_panel_doc_tables.py --write", file=sys.stderr)
            return 1
        duplicated = pages_rendering_section_tabs()
        if duplicated:
            print(
                "<SectionTabs> rendered inside a page:\n  " + "\n  ".join(duplicated),
                file=sys.stderr,
            )
            print(
                "\nThe shell (components/Layout.tsx) already renders the active section's"
                " tab bar from nav.ts, so these pages show it twice. Remove <SectionTabs>"
                " from each listed page.",
                file=sys.stderr,
            )
            return 1
        print("generated admin-panel doc tables are current")
        print("no page renders its own section tab bar")
        return 0

    if args.write:
        changed = []
        for doc_path in docs:
            if not doc_path.exists():
                continue
            doc = doc_path.read_text(encoding="utf-8")
            if NOTE not in doc:
                continue
            for name, body in blocks.items():
                if BEGIN.format(block=name) in doc and splice(doc_path, name, body):
                    changed.append(f"{doc_path.name}: {name}")
        print("rewrote:\n  " + "\n  ".join(changed) if changed else "nothing to rewrite")
        return 0

    for key in ("sections", "permissions"):
        print(f"### {key}\n")
        print(tables[key])
        print()
    print(
        f"// {tables['section_count']} sections, {tables['tab_count']} tabs, "
        f"{tables['route_count']} routes, {tables['assignable_count']} assignable rights, "
        f"{tables['permission_count']} labelled; unlabelled: {tables['unlabelled']}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())