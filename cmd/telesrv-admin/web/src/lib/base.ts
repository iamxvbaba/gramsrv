/// <reference types="vite/client" />
// The panel can be served under a path prefix (vite build --base=/prefix/).
// Routes, API calls and asset URLs in the code are written from the root;
// withBase adds the prefix on the way out and stripBase removes it from the
// address bar so routing keeps seeing root paths.
export const BASE = (import.meta.env.BASE_URL || "/").replace(/\/+$/, "");

export function withBase(path: string): string {
  if (!BASE || !path.startsWith("/") || path.startsWith("//")) return path;
  if (path === BASE || path.startsWith(`${BASE}/`)) return path;
  return `${BASE}${path}`;
}

export function stripBase(pathname: string): string {
  if (!BASE) return pathname;
  if (pathname === BASE) return "/";
  if (pathname.startsWith(`${BASE}/`)) return pathname.slice(BASE.length);
  return pathname;
}
