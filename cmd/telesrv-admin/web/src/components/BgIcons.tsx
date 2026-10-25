import { useEffect, useRef } from "react";

// The drifting icon field from the marketing site, ported so the console's
// sign-in screen looks like the same product rather than a different one.
//
// Same 28 particles, same sizes, speeds, opacities and repulsion as the
// original -- this is meant to match, not to be a second interpretation of the
// idea.

// Inner markup for each icon, drawn as strokes on a 24x24 viewBox. These are
// source constants, never anything a user supplied, which is what makes
// injecting them as HTML below safe.
const ICON_PATHS = [
  `<rect x="2.5" y="3.5" width="19" height="12" rx="2"/><path d="M8 20h8M12 15.5V20"/>`,
  `<rect x="3.5" y="4" width="17" height="6" rx="2"/><rect x="3.5" y="14" width="17" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>`,
  `<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="M3 7l9 6.5L21 7"/>`,
  `<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.6 2.5 4 5.7 4 9s-1.4 6.5-4 9c-2.6-2.5-4-5.7-4-9s1.4-6.5 4-9z"/>`,
  `<rect x="7" y="3" width="10" height="18" rx="2.4"/><path d="M11 18h2"/>`,
  `<path d="M8 6l-6 6 6 6M16 6l6 6-6 6"/>`,
  `<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M18.5 15.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z"/>`,
  `<path d="M12 3l7 3v5c0 4.5-3 7.6-7 9-4-1.4-7-4.5-7-9V6l7-3z"/><path d="M8.8 12.2l2.1 2.1 4.3-4.3"/>`,
  `<path d="M21.5 3.5l-19 7.5 5.5 2.5 3 5 3-2 5.5 5z"/><path d="M11 14l8.5-8.5"/>`,
  `<rect x="4.5" y="11" width="15" height="9" rx="2.2"/><path d="M8 11V8a4 4 0 0 1 7.5-1.9"/><path d="M12 15v2"/>`,
  `<path d="M12 3v12"/><path d="M7.5 10.5L12 15l4.5-4.5"/><path d="M5 20h14"/>`,
  `<circle cx="12" cy="12" r="2.5"/><path d="M12 2v4M12 18v4M2 12h4M18 12h4M4.9 4.9l2.8 2.8M16.3 16.3l2.8 2.8M4.9 19.1l2.8-2.8M16.3 7.7l2.8-2.8"/>`,
  `<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>`,
  `<path d="M12 2l3.1 6.3L22 9.5l-5 4.9 1.2 7L12 17.3 5.8 21.4 7 14.4l-5-4.9 6.9-1.2z"/>`,
  `<path d="M14.5 17.5L19 13l-4.5-4.5M9.5 6.5L5 11l4.5 4.5"/>`,
  `<polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/>`,
  `<path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/><path d="M15 12h4M17 10v4"/>`,
  `<rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/>`,
  `<circle cx="12" cy="12" r="2"/><path d="M12 2v4M12 18v4M2 12h4M18 12h4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/>`,
  `<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/>`,
  `<path d="M21 11.5a8.38 8.38 0 01-.9 3.8 8.5 8.5 0 01-7.6 4.7 8.38 8.38 0 01-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 01-.9-3.8 8.5 8.5 0 014.7-7.6 8.38 8.38 0 013.8-.9h.5a8.48 8.48 0 018 8v.5z"/>`,
  `<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/><line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/><line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="14" x2="23" y2="14"/><line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="14" x2="4" y2="14"/>`,
  `<path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>`,
  `<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/>`,
  `<circle cx="12" cy="12" r="10"/><path d="M12 6v12M6 12h12"/>`
];

const PARTICLE_COUNT = 28;

type Particle = {
  x: number;
  y: number;
  vx: number;
  vy: number;
  size: number;
  rot: number;
  rotSpeed: number;
};

export function BgIcons() {
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    // Someone who asked the system for less motion gets a still field rather
    // than twenty-eight things drifting across their screen.
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    const nodes = Array.from(host.querySelectorAll<SVGSVGElement>(".bg-icon"));
    let particles: Particle[] = [];

    function seed() {
      const w = window.innerWidth;
      const h = window.innerHeight;
      const margin = 60;
      particles = nodes.map((node) => {
        const size = 22 + Math.random() * 30;
        node.style.width = `${size}px`;
        node.style.height = `${size}px`;
        node.style.opacity = String(0.18 + Math.random() * 0.12);
        return {
          x: margin + Math.random() * Math.max(1, w - margin * 2),
          y: margin + Math.random() * Math.max(1, h - margin * 2),
          vx: (Math.random() - 0.5) * 0.3,
          vy: (Math.random() - 0.5) * 0.3,
          size,
          rot: Math.random() * 360,
          rotSpeed: (Math.random() - 0.5) * 0.08
        };
      });
    }

    let frame = 0;
    function tick() {
      const w = window.innerWidth;
      const h = window.innerHeight;

      for (let i = 0; i < particles.length; i++) {
        const a = particles[i];
        a.x += a.vx;
        a.y += a.vy;
        a.rot += a.rotSpeed;

        // Bounce off the viewport edges so the field stays on screen.
        const half = a.size / 2;
        if (a.x < half) { a.x = half; a.vx *= -1; }
        if (a.x > w - half) { a.x = w - half; a.vx *= -1; }
        if (a.y < half) { a.y = half; a.vy *= -1; }
        if (a.y > h - half) { a.y = h - half; a.vy *= -1; }

        // Gentle mutual repulsion: without it the drift eventually clumps the
        // icons into a corner and the field stops reading as a field.
        for (let j = i + 1; j < particles.length; j++) {
          const b = particles[j];
          const dx = b.x - a.x;
          const dy = b.y - a.y;
          const dist = Math.sqrt(dx * dx + dy * dy);
          const minDist = (a.size + b.size) / 2 + 20;
          if (dist < minDist && dist > 0.01) {
            const force = ((minDist - dist) / minDist) * 0.02;
            const nx = dx / dist;
            const ny = dy / dist;
            a.vx -= nx * force;
            a.vy -= ny * force;
            b.vx += nx * force;
            b.vy += ny * force;
          }
        }
      }

      // Written straight to the nodes: this runs every frame, and putting 28
      // positions through React state would re-render the sign-in form sixty
      // times a second to move some background art.
      for (let i = 0; i < nodes.length; i++) {
        const p = particles[i];
        nodes[i].style.transform = `translate(${p.x}px, ${p.y}px) rotate(${p.rot}deg)`;
      }
      frame = requestAnimationFrame(tick);
    }

    seed();
    frame = requestAnimationFrame(tick);
    window.addEventListener("resize", seed);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", seed);
    };
  }, []);

  return (
    <div className="bg-icons" aria-hidden="true" ref={hostRef}>
      {Array.from({ length: PARTICLE_COUNT }, (_, i) => (
        <svg
          key={i}
          className="bg-icon"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
          dangerouslySetInnerHTML={{ __html: ICON_PATHS[i % ICON_PATHS.length] }}
        />
      ))}
    </div>
  );
}