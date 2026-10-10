import clsx from "clsx";
import { useEffect, useState } from "react";
import logo from "../assets/logo.webp";

/**
 * The backdrop behind the glass, "dawn": warm light from the left — mandarin, rose,
 * honey — and cool from the right — lilac, sky. The two drift apart slowly; paper grain
 * on top. `calm` keeps it still (the subscription page, see .atmo.calm in app.css).
 */
export function Atmosphere({ calm }: { calm?: boolean }) {
  const idle = useIdle();
  return (
    <div className={clsx("atmo", calm && "calm", idle && "idle")} aria-hidden>
      <span className="glow glow-warm" />
      <span className="glow glow-cool" />
      <span className="pattern" />
      <span className="grain" />
    </div>
  );
}

/** How long without a key, a click or a move before the backdrop stops drifting. */
const IDLE_AFTER = 60_000;

/**
 * Whether nobody is looking: the window lost focus (another app, a second monitor) or there
 * was no input for a minute. The drifting glows under the blurred glass make the browser
 * redraw every card every frame; a panel left open stops costing that.
 */
function useIdle(): boolean {
  const [idle, setIdle] = useState(false);
  useEffect(() => {
    let timer = 0;
    let woke = 0;
    const wake = () => {
      // A moving pointer fires dozens of times a second: once a second is enough.
      const now = Date.now();
      if (now - woke < 1000) return;
      woke = now;
      setIdle(false);
      window.clearTimeout(timer);
      timer = window.setTimeout(() => setIdle(true), IDLE_AFTER);
    };
    const sleep = () => {
      window.clearTimeout(timer);
      woke = 0;
      setIdle(true);
    };
    const events = ["pointermove", "pointerdown", "keydown", "wheel", "touchstart", "focus"] as const;
    events.forEach((e) => window.addEventListener(e, wake, { passive: true }));
    window.addEventListener("blur", sleep);
    wake();
    return () => {
      window.clearTimeout(timer);
      events.forEach((e) => window.removeEventListener(e, wake));
      window.removeEventListener("blur", sleep);
    };
  }, []);
  return idle;
}

/** The painted mandarin on a transparent ground: it reads the same in every theme. */
export function Logo({ size = 32 }: { size?: number }) {
  return <img src={logo} width={size} height={size} alt="" aria-hidden decoding="async" draggable={false} className="shrink-0" />;
}
