import { useEffect, useRef } from "react";
import { BgIcons } from "./BgIcons";

// The product's background: three blurred orbs that drift with the pointer,
// and the field of slowly moving icons over them. Ported from the marketing
// site so the console reads as the same product.
//
// One component for both places it appears -- the sign-in screen and the
// workspace behind every page -- because two copies of a parallax handler is
// how they end up subtly different.
export function AppBackground({ className = "" }: { className?: string }) {
  const orbsRef = useRef<HTMLDivElement>(null);

  // The orbs drift with the pointer, the same 30px parallax the site uses.
  // Written straight to the node instead of through state: this fires on every
  // mouse move, and re-rendering the tree for a background offset would be a
  // lot of work to move three blurred circles.
  useEffect(() => {
    // Someone who asked the system for less motion gets none of this. The CSS
    // drift is already disabled for them, and a JS-driven transform would walk
    // straight past that preference.
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      return;
    }
    function onMove(event: MouseEvent) {
      const node = orbsRef.current;
      if (!node) return;
      const x = (event.clientX / window.innerWidth - 0.5) * 30;
      const y = (event.clientY / window.innerHeight - 0.5) * 30;
      node.style.transform = `translate(${x}px, ${y}px)`;
    }
    window.addEventListener("mousemove", onMove);
    return () => window.removeEventListener("mousemove", onMove);
  }, []);

  return (
    <div className={`app-background ${className}`.trim()} aria-hidden="true">
      <div className="bg-orbs" ref={orbsRef}>
        <div className="bg-orb bg-orb--1" />
        <div className="bg-orb bg-orb--2" />
        <div className="bg-orb bg-orb--3" />
      </div>
      <BgIcons />
    </div>
  );
}