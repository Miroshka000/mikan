// An addon's own page in the admin panel (docs/pipeline/addons.md, section 9). The panel
// serves the addon's files at a signed address into a frame sandboxed without
// allow-same-origin: the page gets an opaque origin, so no cookie, no storage, no admin API
// and no reach into this window. What it needs from its addon it asks this window for over
// postMessage, and this window makes the call with the admin's session.
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { Blocks, RefreshCw, ShieldOff, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState, type RefObject } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../api/client";
import { qk } from "../../api/hooks";
import { Button, EmptyState, ErrorState, PageHeader, Skeleton } from "../../components/ui";
import { getLocale, t } from "../../i18n";
import { localized } from "../../lib/localized";
import { BackToAddons } from "./addon-row";
import { ADDON_ID_RE } from "./market-common";

type Page = Schemas["AddonPageView"];

const METHODS = new Set(["GET", "POST", "PUT", "DELETE"]);
/** A page that floods the panel with calls gets 429 for the extra ones. */
const MAX_CALLS = 8;
/** The signed address lets the frame load files for ten minutes. */
const ADDRESS_TTL = 10 * 60_000;

export function AddonAppPage() {
  const { id = "" } = useParams({ strict: false }) as { id?: string };
  const valid = ADDON_ID_RE.test(id);
  const q = useQuery({
    queryKey: qk.addonPage(id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/addons/{id}/ui", { params: { path: { id } }, signal })),
    enabled: valid,
    // Every visit asks for a fresh address; while the page is open it is not refetched on its
    // own, since a new address reloads the frame under the admin's hands.
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: (n, e) => !(e instanceof ApiError && e.status > 0 && e.status < 500) && n < 2,
  });
  const title = q.data ? localized(q.data.title, id) : id;
  return (
    <>
      <PageHeader
        title={title}
        sub={t("addonApp.sub", { id })}
        actions={
          <>
            {q.data ? (
              <Button onClick={() => void q.refetch()} loading={q.isRefetching}>
                <RefreshCw size={16} aria-hidden />
                {t("addonApp.reload")}
              </Button>
            ) : null}
            <BackToAddons />
          </>
        }
      />
      {!valid ? (
        <NoPages id={id} />
      ) : q.data ? (
        <AddonFrame key={q.data.url} id={id} page={q.data} title={title} reloading={q.isRefetching} onReload={() => void q.refetch()} />
      ) : q.isError ? (
        <PageError id={id} error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <FrameSkeleton />
      )}
    </>
  );
}

function NoPages({ id }: { id: string }) {
  return (
    <section className="card glass">
      <EmptyState icon={Blocks} title={t("addonApp.noPagesTitle")} text={t("addonApp.noPagesText", { id })} />
    </section>
  );
}

function PageError({ id, error, onRetry }: { id: string; error: unknown; onRetry: () => void }) {
  const status = error instanceof ApiError ? error.status : 0;
  if (status === 404 || status === 422) return <NoPages id={id} />;
  return (
    <section className="card glass">
      {status === 403 ? (
        <EmptyState icon={ShieldOff} title={t("addonApp.forbiddenTitle")} text={t("addonApp.forbiddenText")} />
      ) : status === 502 ? (
        <ErrorState title={t("addonApp.unreachableTitle")} text={t("addonApp.unreachableText")} onRetry={onRetry} />
      ) : (
        <ErrorState text={errorText(error)} onRetry={onRetry} />
      )}
    </section>
  );
}

/** The page fills the content area below the header, but never gets too short to use. */
const frameHeight = "max(480px, calc(100dvh - 176px))";

function FrameSkeleton() {
  return (
    <div className="flex flex-col gap-4" style={{ height: frameHeight }} role="status" aria-busy aria-label={t("addonApp.loading")}>
      <Skeleton style={{ height: 48, borderRadius: 16 }} />
      <Skeleton style={{ flex: 1, borderRadius: 20 }} />
    </div>
  );
}

function AddonFrame({ id, page, title, reloading, onReload }: { id: string; page: Page; title: string; reloading: boolean; onReload: () => void }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const loads = useRef(0);
  const [loaded, setLoaded] = useState(false);
  const [expired, setExpired] = useState(false);
  useBridge(frame, loads, id);
  useEffect(() => {
    // The server's clock decides; a client clock far off still gets a sane wait.
    const left = Math.min(Math.max(page.expires_at * 1000 - Date.now(), 60_000), ADDRESS_TTL);
    const timer = window.setTimeout(() => setExpired(true), left);
    return () => window.clearTimeout(timer);
  }, [page.expires_at]);
  return (
    <>
      {expired ? (
        <div className="banner warn" role="status">
          <TriangleAlert size={16} className="shrink-0" aria-hidden />
          <span className="min-w-0 flex-1">{t("addonApp.expired")}</span>
          <Button size="sm" variant="ghost" loading={reloading} onClick={onReload}>
            {t("addonApp.reload")}
          </Button>
        </div>
      ) : null}
      <section className="card glass relative overflow-hidden" style={{ padding: 0, height: frameHeight }}>
        {!loaded ? (
          <div className="absolute inset-0 flex flex-col gap-4 p-5" role="status" aria-busy aria-label={t("addonApp.loading")}>
            <Skeleton style={{ width: 240, height: 28 }} />
            <Skeleton style={{ height: 96, borderRadius: 16 }} />
            <Skeleton style={{ flex: 1, borderRadius: 16 }} />
          </div>
        ) : null}
        <iframe
          ref={frame}
          src={new URL(page.url, document.baseURI).href}
          title={title}
          // Scripts only: without allow-same-origin the page cannot read the panel's cookie,
          // storage or API, nor this window; the server's CSP sandboxes it again.
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          onLoad={() => {
            loads.current += 1;
            setLoaded(true);
          }}
          className="block h-full w-full border-0"
          style={{ opacity: loaded ? 1 : 0 }}
        />
      </section>
    </>
  );
}

type RPCMessage = { type: "rpc"; id: string | number; method: string; path: string; body?: unknown };

function isRPC(m: unknown): m is RPCMessage {
  if (!m || typeof m !== "object") return false;
  const r = m as Record<string, unknown>;
  return r.type === "rpc" && (typeof r.id === "string" || typeof r.id === "number");
}

/**
 * The frame's calls: taken only from that frame's window with its opaque origin, checked,
 * made with the admin's session (the API client adds the CSRF token) and answered to the
 * same document. An answer for a document the frame has left since is dropped.
 */
function useBridge(frame: RefObject<HTMLIFrameElement | null>, loads: RefObject<number>, id: string) {
  useEffect(() => {
    let inFlight = 0;
    const onMessage = (ev: MessageEvent) => {
      const win = frame.current?.contentWindow;
      if (!win || ev.source !== win || ev.origin !== "null" || !isRPC(ev.data)) return;
      const { id: callID, method, path, body } = ev.data;
      const load = loads.current;
      const reply = (status: number, answer: unknown) => {
        if (loads.current !== load || frame.current?.contentWindow !== win) return;
        win.postMessage({ type: "rpc-result", id: callID, status, body: answer }, "*");
      };
      if (typeof method !== "string" || !METHODS.has(method) || typeof path !== "string" || !path.startsWith("/") || path.length > 1024 || /[\u0000-\u001f\u007f]/.test(path)) {
        reply(400, { code: "bad_request", message: "method is GET, POST, PUT or DELETE; path starts with /" });
        return;
      }
      if (inFlight >= MAX_CALLS) {
        reply(429, { code: "rate_limited", message: "too many calls at once" });
        return;
      }
      inFlight += 1;
      api
        .POST("/api/v1/addons/{id}/ui/rpc", {
          params: { path: { id } },
          body: { method: method as "GET" | "POST" | "PUT" | "DELETE", path, body: body ?? null, lang: getLocale() },
        })
        .then(({ data, error, response }) => {
          if (response.ok && data) reply(data.status, data.body);
          else reply(response.status, { code: (error as { detail?: string } | undefined)?.detail || "error" });
        })
        .catch(() => reply(0, { code: "network" }))
        .finally(() => {
          inFlight -= 1;
        });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [frame, loads, id]);
}
