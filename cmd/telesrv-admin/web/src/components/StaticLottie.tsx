import lottie from "lottie-web/build/player/lottie_light_canvas";
import { useEffect, useRef } from "react";

// StaticLottie renders a single (first) frame of a Lottie/TGS animation instead
// of looping it, so a grid of many stickers/emoji does not keep the canvas
// rendering and pinning the CPU. It plays only while hovered, then resets to the
// static frame. Use it for list/grid previews; keep the looping player for
// single, focused previews.
export function StaticLottie({
  loader,
  cacheKey,
  className,
  playOnHover = true,
  onError
}: {
  loader: () => Promise<Record<string, unknown>>;
  cacheKey: string;
  className?: string;
  playOnHover?: boolean;
  onError?: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const animation = useRef<ReturnType<typeof lottie.loadAnimation> | null>(null);

  useEffect(() => {
    let cancelled = false;
    loader()
      .then((data) => {
        if (cancelled || !host.current) return;
        animation.current?.destroy();
        animation.current = lottie.loadAnimation({
          container: host.current,
          renderer: "canvas",
          loop: true,
          autoplay: false,
          animationData: structuredClone(data)
        });
        animation.current.goToAndStop(0, true);
      })
      .catch(() => onError?.());
    return () => {
      cancelled = true;
      animation.current?.destroy();
      animation.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cacheKey]);

  function play() {
    if (playOnHover) animation.current?.play();
  }
  function reset() {
    if (playOnHover) animation.current?.goToAndStop(0, true);
  }

  return <div className={className} ref={host} onMouseEnter={play} onMouseLeave={reset} />;
}
