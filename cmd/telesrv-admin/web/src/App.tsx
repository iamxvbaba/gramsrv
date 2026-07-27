import { useEffect, useState } from "react";
import { api } from "./api";
import { BootScreen, Shell } from "./components/Layout";
import { LoginPage } from "./pages/LoginPage";
import { PermissionsProvider } from "./permissions";
import { Routes } from "./pages/Routes";
import { currentRoute, type RouteState } from "./routing";
import type { AdminSession } from "./types";

export function App() {
  // One GET /api/session at boot carries both the actor and the permission set the
  // signed session was issued with.
  const [session, setSession] = useState<AdminSession | null | undefined>(undefined);
  const [route, setRoute] = useState<RouteState>(() => currentRoute());

  useEffect(() => {
    const onPopState = () => setRoute(currentRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    api.session()
      .then((next) => setSession(next))
      // A 401 and an unreachable backend both end at the login screen; there is
      // nothing the panel can render without a session.
      .catch(() => setSession(null));
  }, []);

  const navigate = (href: string) => {
    window.history.pushState(null, "", href);
    setRoute(currentRoute());
  };

  if (session === undefined) {
    return <BootScreen />;
  }

  if (session === null) {
    return <LoginPage onLogin={setSession} />;
  }

  return (
    <PermissionsProvider permissions={session.permissions ?? []}>
      <Shell actor={session.actor} route={route} navigate={navigate} onLogout={() => setSession(null)}>
        <Routes route={route} navigate={navigate} />
      </Shell>
    </PermissionsProvider>
  );
}
