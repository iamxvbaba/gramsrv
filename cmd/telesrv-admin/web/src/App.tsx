import { useEffect, useState } from "react";
import { api, APIError } from "./api";
import { BootScreen, Shell } from "./components/Layout";
import { LoginPage } from "./pages/LoginPage";
import { Routes } from "./pages/Routes";
import { currentRoute, type RouteState } from "./routing";

export function App() {
  const [actor, setActor] = useState<string | null | undefined>(undefined);
  const [route, setRoute] = useState<RouteState>(() => currentRoute());

  useEffect(() => {
    const onPopState = () => setRoute(currentRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    api.session()
      .then((session) => setActor(session.actor))
      .catch((error) => {
        if (error instanceof APIError && error.status === 401) {
          setActor(null);
          return;
        }
        setActor(null);
      });
  }, []);

  const navigate = (href: string) => {
    window.history.pushState(null, "", href);
    setRoute(currentRoute());
  };

  if (actor === undefined) {
    return <BootScreen />;
  }

  if (actor === null) {
    return <LoginPage onLogin={setActor} />;
  }

  return (
    <Shell actor={actor} route={route} navigate={navigate} onLogout={() => setActor(null)}>
      <Routes route={route} navigate={navigate} />
    </Shell>
  );
}
