import type { ReactNode } from "react";
import type { Navigate } from "../routing";
import { withBase } from "../lib/base";

export function AppLink({
  href,
  navigate,
  className,
  children
}: {
  href: string;
  navigate: Navigate;
  className?: string;
  children: ReactNode;
}) {
  return (
    <a
      className={className}
      href={withBase(href)}
      onClick={(event) => {
        event.preventDefault();
        navigate(href);
      }}
    >
      {children}
    </a>
  );
}
