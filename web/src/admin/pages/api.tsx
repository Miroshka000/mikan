import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, Copy, KeyRound, Plus, Trash2 } from "lucide-react";
import { lazy, Suspense, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { meQuery, qk } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Field, PageHeader, Segmented, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { fieldErrors } from "../../lib/fields";
import { ago, dateShort } from "../../lib/format";

// The reference parses the whole OpenAPI spec: loaded only when the page opens.
const ApiReference = lazy(() => import("./api-reference"));

type APIKey = Schemas["APIKeyView"];
type Scope = APIKey["scope"];

export function ApiPage() {
  return (
    <>
      <Link to="/settings" search={{ tab: "security" }} className="mb-2 inline-flex items-center gap-1 rounded-lg text-[13px] font-medium text-[var(--ink-500)] hover:text-[var(--ink-900)]">
        <ChevronLeft size={16} aria-hidden /> {t("apiPage.back")}
      </Link>
      <PageHeader title={t("apiPage.title")} sub={t("apiPage.subtitle")} />
      <div className="flex flex-col gap-4">
        <KeysCard />
        <Suspense fallback={<Skeleton style={{ height: 480, borderRadius: 20 }} />}>
          <ApiReference />
        </Suspense>
      </div>
    </>
  );
}

const expiries = [0, 30, 90, 365] as const;

function KeysCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const keys = useQuery({ queryKey: qk.apiKeys, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/api-keys", { signal })) });
  const [adding, setAdding] = useState(false);
  const [made, setMade] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<APIKey | null>(null);
  const revoke = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/api-keys/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.apiKeys });
      setRevoking(null);
      toast.ok(t("apiPage.revoked"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("apiPage.keys")}</h2>
          <div className="card-sub">{t("apiPage.keysSub")}</div>
        </div>
        <Button size="sm" variant="primary" onClick={() => setAdding(true)}>
          <Plus size={16} aria-hidden /> {t("apiPage.newKey")}
        </Button>
      </div>
      <QueryBoundary query={keys} pending={<Skeleton style={{ height: 96 }} />}>
        {(list) =>
          list.length === 0 ? (
            <EmptyState title={t("apiPage.noKeys")} text={t("apiPage.noKeysText")} />
          ) : (
            <ul className="row-list">
              {list.map((k) => (
                <KeyRow key={k.id} k={k} onRevoke={() => setRevoking(k)} />
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      <NewKeyDrawer open={adding} onOpenChange={setAdding} onMade={setMade} />
      <MadeKeyDrawer apiKey={made} onClose={() => setMade(null)} />
      <Confirm
        open={!!revoking}
        onOpenChange={(v) => !v && setRevoking(null)}
        title={t("apiPage.revokeTitle", { name: revoking?.name ?? "" })}
        text={t("apiPage.revokeText")}
        confirm={t("apiPage.revoke")}
        danger
        loading={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </section>
  );
}

function KeyRow({ k, onRevoke }: { k: APIKey; onRevoke: () => void }) {
  const expired = !!k.expires_at && new Date(k.expires_at).getTime() <= Date.now();
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 py-3">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-[13px] font-medium">{k.name}</span>
          <span className="tag">{k.scope === "full" ? t("apiPage.scopeFull") : t("apiPage.scopeRead")}</span>
          {expired ? <span className="pill bad">{t("apiPage.expired")}</span> : null}
        </div>
        <div className="mt-1 text-xs text-[var(--ink-500)]">
          <span className="mono">{k.prefix}…</span> · {t("apiPage.created", { date: dateShort(k.created_at) })}
          {k.expires_at && !expired ? <> · {t("apiPage.expires", { date: dateShort(k.expires_at) })}</> : null}
          {" · "}
          {k.last_used_at ? (
            <>
              {t("apiPage.used", { ago: ago(k.last_used_at) })}
              {k.last_ip ? <span className="mono"> {k.last_ip}</span> : null}
            </>
          ) : (
            t("apiPage.neverUsed")
          )}
        </div>
      </div>
      <Button size="sm" variant="danger" onClick={onRevoke}>
        <Trash2 size={16} aria-hidden /> {t("apiPage.revoke")}
      </Button>
    </li>
  );
}

function NewKeyDrawer({ open, onOpenChange, onMade }: { open: boolean; onOpenChange: (v: boolean) => void; onMade: (key: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("read");
  const [days, setDays] = useState<number>(0);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const me = useQuery(meQuery);
  const totp = !!me.data?.admin.totp_enabled;
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/api-keys", { body: { name: name.trim(), scope, expire_days: days, password, totp: code || undefined } })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.apiKeys });
      onOpenChange(false);
      setName("");
      setScope("read");
      setDays(0);
      setPassword("");
      setCode("");
      onMade(r.key);
    },
  });
  const errors = fieldErrors(create.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <Drawer
      open={open}
      onOpenChange={(v) => {
        if (!v) create.reset();
        onOpenChange(v);
      }}
      title={t("apiPage.newKey")}
      footer={
        <DrawerFooter form="new-key" onCancel={() => onOpenChange(false)} loading={create.isPending} disabled={!name.trim() || !password || (totp && code.length !== 6)}>
          {t("apiPage.create")}
        </DrawerFooter>
      }
    >
      <form id="new-key" onSubmit={submit} className="pt-5" noValidate>
        {create.error && !Object.keys(errors).length ? <div className="banner err mb-4">{errorText(create.error)}</div> : null}
        <Field label={t("apiPage.name")} htmlFor="k-name" hint={t("apiPage.nameHint")} error={errors.name}>
          <input id="k-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder="billing-bot" autoComplete="off" aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("apiPage.scope")} hint={scope === "full" ? t("apiPage.scopeFullHint") : t("apiPage.scopeReadHint")}>
          <Segmented
            label={t("apiPage.scope")}
            value={scope}
            onChange={setScope}
            options={[
              { value: "read", label: t("apiPage.scopeRead") },
              { value: "full", label: t("apiPage.scopeFull") },
            ]}
          />
        </Field>
        <Field label={t("apiPage.expiry")} htmlFor="k-exp">
          <select id="k-exp" className="input max-w-[240px]" value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {expiries.map((d) => (
              <option key={d} value={d}>
                {d === 0 ? t("apiPage.expiryNever") : t("apiPage.expiryDays", { d })}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("apiPage.password")} htmlFor="k-pw" hint={t("apiPage.passwordHint")} error={errors.password}>
          <input id="k-pw" type="password" className="input" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" aria-invalid={!!errors.password} />
        </Field>
        {totp ? (
          <Field label={t("settings.totpCode")} htmlFor="k-totp" error={errors.totp}>
            <input id="k-totp" className="input mono max-w-[160px] tracking-[0.2em]" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.trim())} autoComplete="one-time-code" aria-invalid={!!errors.totp} />
          </Field>
        ) : null}
      </form>
    </Drawer>
  );
}

/** The key is shown once: the panel keeps only its hash. */
function MadeKeyDrawer({ apiKey, onClose }: { apiKey: string | null; onClose: () => void }) {
  const copyText = useCopy();
  const copy = () => apiKey && copyText(apiKey, t("apiPage.keyCopied"));
  return (
    <Drawer
      open={!!apiKey}
      onOpenChange={(v) => !v && onClose()}
      title={t("apiPage.madeTitle")}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t("apiPage.madeDone")}
        </Button>
      }
    >
      <div className="pt-5">
        <div className="banner warn mb-4">
          <KeyRound size={18} className="shrink-0" aria-hidden />
          <span>{t("apiPage.madeOnce")}</span>
        </div>
        <div className="link-field">
          <span className="mono break-all">{apiKey}</span>
          <button type="button" className="icon-btn" onClick={() => void copy()} aria-label={t("apiPage.copyKey")}>
            <Copy size={18} />
          </button>
        </div>
      </div>
    </Drawer>
  );
}
