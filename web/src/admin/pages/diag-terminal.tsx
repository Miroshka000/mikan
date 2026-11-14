// The support operator's terminal: a shell on the panel's server or on a node, over a
// WebSocket through the panel. Everything typed and shown is recorded on the server, and
// the session ends by itself when the admin revokes the access or its time is up.
import { Plug, PlugZap, TerminalSquare, TriangleAlert, Unplug } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { wsUrl } from "../../api/client";
import type { SupportWhoami } from "../../api/support";
import { Button, Field, Pill, Skeleton } from "../../components/ui";
import type { TermHandle } from "../../components/term";
import { tMaybe, t } from "../../i18n";
import { serverName } from "../../lib/node-label";
import { timeLeft } from "../../lib/format";
import { useNow } from "../../lib/now";
import { CardHead } from "../../components/layout";
import { Select } from "../../components/select";

const Term = lazy(() => import("../../components/term"));

type State = "idle" | "connecting" | "live" | "ended";
/** Why a session is over, as the panel tells it. */
type Over = { reason?: string; error?: string };

const encoder = new TextEncoder();

export function TerminalTab({ who }: { who: SupportWhoami }) {
  const targets = who.terminal_targets;
  const [target, setTarget] = useState(targets[0] ?? "panel");
  const [state, setState] = useState<State>("idle");
  const [over, setOver] = useState<Over>({});
  const [session, setSession] = useState(0);
  const [handle, setHandle] = useState<TermHandle | null>(null);
  const ws = useRef<WebSocket | null>(null);
  // The server of the session being opened: changing the select afterwards opens nothing.
  const opening = useRef(target);
  const userClosed = useRef(false);
  const actions = useRef<HTMLDivElement>(null);
  const now = useNow(15_000);
  const ended = new Date(who.expires_at).getTime() <= now;

  const onReady = useCallback((h: TermHandle) => setHandle(h), []);

  // One WebSocket per press of "Connect"; leaving the page or pressing "Disconnect" closes it.
  useEffect(() => {
    if (!session || !handle) return;
    handle.reset();
    let opened = false;
    let settled = false;
    const socket = new WebSocket(wsUrl(`/api/v1/support/terminal?target=${encodeURIComponent(opening.current)}`));
    socket.binaryType = "arraybuffer";
    ws.current = socket;
    socket.onmessage = (e) => {
      if (typeof e.data !== "string") {
        handle.write(new Uint8Array(e.data as ArrayBuffer));
        return;
      }
      let m: { type?: string; reason?: string; code?: string };
      try {
        m = JSON.parse(e.data);
      } catch {
        return;
      }
      if (m.type === "ready") {
        opened = true;
        setState("live");
        const { cols, rows } = handle.size();
        socket.send(JSON.stringify({ type: "resize", cols, rows }));
        handle.focus();
      } else if (m.type === "error") {
        settled = true;
        setOver({ error: m.code });
        setState("ended");
      } else if (m.type === "end") {
        settled = true;
        setOver({ reason: m.reason });
        setState("ended");
      }
    };
    socket.onclose = () => {
      ws.current = null;
      if (settled) return;
      if (userClosed.current) {
        setOver({ reason: "closed" });
      } else {
        // Closed with no word from the panel: it never opened (refused, no link) or the link broke.
        setOver({ error: opened ? "terminal_closed" : "connect" });
      }
      setState("ended");
    };
    return () => {
      settled = true;
      socket.onclose = null;
      socket.onmessage = null;
      socket.close(1000);
      if (ws.current === socket) ws.current = null;
    };
  }, [session, handle]);

  const connect = () => {
    opening.current = target;
    userClosed.current = false;
    setOver({});
    setState("connecting");
    setSession((n) => n + 1);
  };
  const disconnect = () => {
    userClosed.current = true;
    ws.current?.close(1000);
  };

  const onData = useCallback((d: string) => {
    const s = ws.current;
    if (s && s.readyState === WebSocket.OPEN) s.send(encoder.encode(d));
  }, []);
  const onResize = useCallback((cols: number, rows: number) => {
    const s = ws.current;
    if (s && s.readyState === WebSocket.OPEN) s.send(JSON.stringify({ type: "resize", cols, rows }));
  }, []);
  // Shift+Esc moves the focus out of the terminal (it keeps Tab for the shell).
  const onLeave = useCallback(() => actions.current?.querySelector("button")?.focus(), []);

  const busy = state === "connecting" || state === "live";
  return (
    <section className="card glass reveal" aria-labelledby="diag-term-title">
      <CardHead title={t("diag.terminal.title")} sub={t("diag.terminal.lead")} aside={<StateMark state={state} />} id="diag-term-title" />
      <div className="banner warn mb-4" role="note">
        <TriangleAlert size={18} className="shrink-0" aria-hidden />
        <span>{t("diag.terminal.recorded", { left: ended ? t("time.lessMinute") : timeLeft(who.expires_at, now) })}</span>
      </div>
      {targets.length === 0 ? (
        <p className="text-[13px] text-[var(--ink-600)]">{t("diag.terminal.noTargets")}</p>
      ) : (
        <>
          <div className="flex flex-wrap items-end gap-3">
            <Field label={t("diag.server")} htmlFor="term-target">
              <Select id="term-target" className="input min-w-[220px]" value={target} disabled={busy} onChange={(e) => setTarget(e.target.value)}>
                {targets.map((x) => (
                  <option key={x} value={x}>
                    {serverName(x, who.nodes)}
                  </option>
                ))}
              </Select>
            </Field>
            <div ref={actions} className="mb-4 flex flex-wrap gap-2">
              {busy ? (
                <Button variant="danger" onClick={disconnect}>
                  <Unplug size={16} aria-hidden /> {t("diag.terminal.disconnect")}
                </Button>
              ) : (
                <Button variant="primary" onClick={connect} disabled={ended}>
                  <Plug size={16} aria-hidden /> {state === "ended" ? t("diag.terminal.again") : t("diag.terminal.connect")}
                </Button>
              )}
            </div>
          </div>
          {ended ? (
            <p className="mb-3 text-[13px] text-[var(--berry-600)]" role="alert">
              {t("diag.terminal.grantOver")}
            </p>
          ) : null}
          <Result state={state} over={over} />
          {state === "idle" ? (
            <div className="term-idle">
              <TerminalSquare size={22} aria-hidden />
              <span>{t("diag.terminal.idle")}</span>
            </div>
          ) : (
            <Suspense fallback={<Skeleton style={{ height: 360, borderRadius: 12 }} />}>
              <Term onReady={onReady} onData={onData} onResize={onResize} onLeave={onLeave} label={t("diag.terminal.screen", { name: serverName(target, who.nodes) })} />
            </Suspense>
          )}
          <p className="mt-3 text-xs text-[var(--ink-500)]">{t("diag.terminal.keys")}</p>
        </>
      )}
    </section>
  );
}

function StateMark({ state }: { state: State }) {
  if (state === "live") return <Pill tone="ok">{t("diag.terminal.live")}</Pill>;
  if (state === "connecting") return <Pill tone="warn">{t("diag.terminal.connecting")}</Pill>;
  if (state === "ended") return <Pill tone="off">{t("diag.terminal.over")}</Pill>;
  return null;
}

/** How a session ended: a normal end in words, a failure with what to do. */
function Result({ state, over }: { state: State; over: Over }) {
  if (state === "connecting") {
    return (
      <p className="mb-3 flex items-center gap-2 text-[13px] text-[var(--ink-600)]" role="status">
        <PlugZap size={16} aria-hidden /> {t("diag.terminal.opening")}
      </p>
    );
  }
  if (state !== "ended") return null;
  if (over.error) {
    return (
      <div className="banner err mb-3" role="alert">
        <TriangleAlert size={18} className="shrink-0" aria-hidden />
        <span>{tMaybe(`diag.terminal.err.${over.error}`) ?? tMaybe(`errors.api.${over.error}`) ?? t("diag.terminal.err.unknown")}</span>
      </div>
    );
  }
  return (
    <p className="mb-3 text-[13px] text-[var(--ink-600)]" role="status">
      {tMaybe(`diag.terminal.end.${over.reason ?? ""}`) ?? t("diag.terminal.end.closed")}
    </p>
  );
}
