import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Link2, RefreshCw, Trash2 } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { ApiError, api, apiCodeText, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useAddonSources } from "../../api/hooks";
import { Confirm, Drawer } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Skeleton } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { fieldErrors } from "../../lib/fields";
import { ago } from "../../lib/format";
import { ShieldBanner } from "./market-parts";

type Sources = Schemas["SourcesView"];
type Source = Schemas["SourceView"];

// The sources of third-party addons: an author's file, catalog or GitHub repository. A source
// decides which code is offered to run on the server, so the drawer keeps in front of the
// admin whose key vouches for it (or that nothing does) and how the last read went.

/** Everything the panel needs from the admin to take a source: its address, and the author's key if they know it. */
function AddForm({ full, limit, onAdded }: { full: boolean; limit: number | undefined; onAdded: (v: Sources) => void }) {
  const uid = useId();
  const toast = useToast();
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [tried, setTried] = useState(false);
  const add = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/addon-sources", { body: { url: url.trim(), key: key.trim() || undefined } })),
    onSuccess: (v) => {
      setUrl("");
      setKey("");
      setTried(false);
      onAdded(v);
      toast.ok(t("sources.added"));
    },
  });
  const server = fieldErrors(add.error);
  const values = add.error instanceof ApiError ? add.error.values : {};
  const extra = Array.isArray(values.url) ? (values.url as unknown[]) : [];
  const general = add.error && !Object.keys(server).length ? errorText(add.error) : "";
  const local = !url.trim() ? t("sources.urlRequired") : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (local || add.isPending || full) return;
    add.mutate();
  };
  return (
    <form onSubmit={submit} noValidate className="flex flex-col gap-4" aria-label={t("sources.addTitle")}>
      {general ? (
        <div className="banner err" role="alert">
          {general}
        </div>
      ) : null}
      <Field label={t("sources.url")} htmlFor={`${uid}-url`} hint={t("sources.urlHint")} error={server.url ?? (tried ? local : undefined)}>
        <input
          id={`${uid}-url`}
          className="input mono"
          value={url}
          maxLength={512}
          placeholder="github.com/owner/repo"
          autoComplete="off"
          spellCheck={false}
          inputMode="url"
          aria-invalid={!!server.url || (tried && !!local)}
          disabled={add.isPending}
          onChange={(e) => setUrl(e.target.value)}
        />
      </Field>
      {extra.length ? <SkippedList items={extra} /> : null}
      <Field label={t("sources.key")} htmlFor={`${uid}-key`} hint={t("sources.keyHint")} error={server.key}>
        <input
          id={`${uid}-key`}
          className="input mono"
          value={key}
          maxLength={64}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!!server.key}
          disabled={add.isPending}
          onChange={(e) => setKey(e.target.value)}
        />
      </Field>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" type="submit" loading={add.isPending} disabled={full}>
          <Link2 size={16} aria-hidden /> {t("sources.add")}
        </Button>
        {full ? <span className="text-xs text-[var(--ink-500)]">{t("sources.full", { n: limit ?? "" })}</span> : null}
        {add.isPending ? <span className="text-xs text-[var(--ink-500)]">{t("sources.reading")}</span> : null}
      </div>
    </form>
  );
}

/** What a failed add names: ids the catalog or another source holds, or entries left out and why. */
function SkippedList({ items }: { items: unknown[] }) {
  return (
    <ul className="-mt-2 flex flex-col gap-1 text-xs text-[var(--ink-500)]" aria-label={t("sources.skippedTitle")}>
      {items.map((it, i) => {
        const o = typeof it === "string" ? { id: it, reason: "" } : (it as { id?: string; reason?: string });
        return (
          <li key={`${o.id}-${i}`}>
            <span className="mono">{o.id}</span>
            {o.reason ? `: ${tMaybe(`sources.skip.${o.reason}`) ?? o.reason}` : null}
          </li>
        );
      })}
    </ul>
  );
}

function SourceCard({ s, index, onChanged }: { s: Source; index: number; onChanged: (v: Sources) => void }) {
  const toast = useToast();
  const [remove, setRemove] = useState(false);
  const [accept, setAccept] = useState(false);
  const refresh = useMutation({
    mutationFn: (fingerprint?: string) =>
      unwrap(api.POST("/api/v1/addon-sources/{id}/refresh", { params: { path: { id: s.id }, query: fingerprint ? { accept: fingerprint } : {} } })),
    onSuccess: (v) => {
      onChanged(v);
      setAccept(false);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const drop = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/addon-sources/{id}", { params: { path: { id: s.id } } })),
    onSuccess: (v) => {
      onChanged(v);
      setRemove(false);
      toast.ok(t("sources.removed"));
    },
    onError: (e) => {
      setRemove(false);
      toast.error(errorText(e));
    },
  });
  const inUse = s.installed.length > 0;
  const keyChanged = s.error === "source_key_changed";
  const entries = s.entries;
  return (
    <li className="reveal flex min-w-0 flex-col gap-3 rounded-[12px] border border-[var(--hairline)] bg-[var(--surface-soft)] p-4" style={{ "--i": Math.min(index, 8) } as React.CSSProperties}>
      <div className="flex flex-wrap items-start gap-2">
        <div className="min-w-0 flex-1">
          <h3 className="addon-card-title">{s.title}</h3>
          <div className="mono mt-1 break-all text-xs text-[var(--ink-500)]">{s.url}</div>
        </div>
        {s.signed ? <Pill tone="ok">{t("sources.signed")}</Pill> : <Pill tone="warn">{t("sources.unsigned")}</Pill>}
      </div>
      {s.signed ? (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <KeyRound size={14} className="shrink-0 text-[var(--ink-500)]" aria-hidden />
          <span className="text-[var(--ink-500)]">{t("sources.fingerprint")}</span>
          <span className="mono break-all">{s.fingerprint}</span>
        </div>
      ) : (
        <ShieldBanner>{t("sources.unsignedWarn")}</ShieldBanner>
      )}
      {s.error ? (
        <div className="banner err flex-wrap items-start" role="alert">
          <span className="min-w-[min(100%,240px)] flex-1 break-words">
            {t("sources.lastError")}: {apiCodeText(s.error)}
            {keyChanged && s.new_fingerprint ? (
              <>
                {" "}
                {t("sources.newKey")} <span className="mono break-all">{s.new_fingerprint}</span>
              </>
            ) : null}
          </span>
          {keyChanged && s.new_fingerprint ? (
            <Button size="sm" onClick={() => setAccept(true)} disabled={refresh.isPending}>
              {t("sources.acceptKey")}
            </Button>
          ) : null}
        </div>
      ) : null}
      <div className="text-xs text-[var(--ink-500)]">
        {[
          s.fetched_at ? t("sources.fetched", { when: ago(s.fetched_at) }) : t("sources.notFetched"),
          t("sources.entriesN", { n: entries }),
          inUse ? t("sources.installedN", { n: s.installed.length }) : null,
        ]
          .filter(Boolean)
          .join(" · ")}
      </div>
      {s.skipped.length ? (
        <details className="text-xs text-[var(--ink-500)]">
          <summary className="cursor-pointer">{t("sources.skippedN", { n: s.skipped.length })}</summary>
          <SkippedList items={s.skipped} />
        </details>
      ) : null}
      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--hairline)] pt-3">
        {inUse ? <span className="mr-auto min-w-0 text-xs text-[var(--ink-500)]">{t("sources.inUseHint")}</span> : null}
        <Button size="sm" loading={refresh.isPending} onClick={() => refresh.mutate(undefined)} aria-label={t("sources.refreshLabel", { name: s.title })}>
          <RefreshCw size={16} aria-hidden /> {t("sources.refresh")}
        </Button>
        <Button size="sm" variant="danger" disabled={inUse} onClick={() => setRemove(true)} aria-label={t("sources.removeLabel", { name: s.title })}>
          <Trash2 size={16} aria-hidden /> {t("sources.remove")}
        </Button>
      </div>
      <Confirm
        open={remove}
        onOpenChange={setRemove}
        title={t("sources.removeTitle", { name: s.title })}
        text={t("sources.removeText")}
        confirm={t("sources.remove")}
        danger
        loading={drop.isPending}
        onConfirm={() => drop.mutate()}
      />
      <Confirm
        open={accept}
        onOpenChange={setAccept}
        title={t("sources.acceptTitle", { name: s.title })}
        text={t("sources.acceptText", { was: s.fingerprint ?? "", now: s.new_fingerprint ?? "" })}
        confirm={t("sources.acceptKey")}
        danger
        loading={refresh.isPending}
        onConfirm={() => refresh.mutate(s.new_fingerprint)}
      />
    </li>
  );
}

function SourcesSkeleton() {
  return (
    <ul className="flex flex-col gap-3" aria-busy aria-label={t("common.loading")}>
      {[0, 1].map((i) => (
        <li key={i} className="flex flex-col gap-3 rounded-[12px] border border-[var(--hairline)] p-4">
          <Skeleton style={{ height: 16, width: "40%", borderRadius: 8 }} />
          <Skeleton style={{ height: 12, width: "80%", borderRadius: 8 }} />
          <Skeleton style={{ height: 32, width: 120, borderRadius: 10 }} />
        </li>
      ))}
    </ul>
  );
}

/** The sources of third-party addons: add by address, see whose key vouches for each, refresh, remove. */
export function SourcesDrawer({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const qc = useQueryClient();
  const q = useAddonSources(open);
  const d = q.data;
  // A source added, refreshed or removed changes what the catalog lists.
  const changed = (v: Sources) => {
    qc.setQueryData(qk.addonSources, v);
    void qc.invalidateQueries({ queryKey: qk.marketplace });
  };
  return (
    <Drawer open={open} onOpenChange={onOpenChange} wide title={t("sources.title")} meta={t("sources.meta")}>
      <div className="flex flex-col gap-4 pt-1 text-[13px]">
        <ShieldBanner>{t("sources.warn")}</ShieldBanner>
        <AddForm full={!!d && d.sources.length >= d.limit} limit={d?.limit} onAdded={changed} />
        <h3 className="font-semibold">{t("sources.listTitle")}</h3>
        {d?.catalog_error ? (
          <div className="banner warn" role="status">
            {apiCodeText(d.catalog_error)}
          </div>
        ) : null}
        {q.isError && !d ? (
          <ErrorState text={errorText(q.error)} onRetry={q.isFetching ? undefined : () => void q.refetch()} />
        ) : !d ? (
          <SourcesSkeleton />
        ) : d.sources.length === 0 ? (
          <EmptyState icon={Link2} title={t("sources.empty")} text={t("sources.emptyText")} />
        ) : (
          <ul className="flex flex-col gap-3" aria-label={t("sources.listTitle")}>
            {d.sources.map((s, i) => (
              <SourceCard key={s.id} s={s} index={i} onChanged={changed} />
            ))}
          </ul>
        )}
      </div>
    </Drawer>
  );
}
