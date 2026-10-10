// An own TLS certificate (GitHub issue #9) for the panel or a node: the chain and its
// key from files or pasted. The key goes to the panel once and never comes back.
import { useMutation } from "@tanstack/react-query";
import { FileKey2, FileText, ShieldCheck, Upload } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { ApiError, errorText } from "../api/client";
import { getLocale, t, tMaybe } from "../i18n";
import { fieldErrors } from "../lib/fields";
import { Confirm, Drawer } from "./overlay";
import { useToast } from "./toast";
import { Button, Field, Pill } from "./ui";

export type CertInfo = { names?: string[]; issuer?: string; not_after?: string; trusted?: boolean; error?: string };

const MAX = 64 << 10;

export function certUntil(iso?: string): string {
  return iso ? new Date(iso).toLocaleDateString(getLocale(), { day: "numeric", month: "long", year: "numeric" }) : "";
}

/** Days left, negative once expired. */
function certDaysLeft(iso?: string): number {
  return iso ? Math.floor((new Date(iso).getTime() - Date.now()) / 86_400_000) : 0;
}

export function CertDrawer({
  open,
  onClose,
  title,
  meta,
  current,
  lead,
  save,
  clear,
  clearLabel,
  clearText,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  meta?: string;
  current: CertInfo | null;
  lead: string;
  save: (cert: string, key: string) => Promise<unknown>;
  clear: () => Promise<unknown>;
  clearLabel: string;
  clearText: string;
}) {
  const toast = useToast();
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const [keyFile, setKeyFile] = useState("");
  const [certFile, setCertFile] = useState("");
  const [clearing, setClearing] = useState(false);
  useEffect(() => {
    if (open) {
      setCert("");
      setKey("");
      setKeyFile("");
      setCertFile("");
    }
  }, [open]);
  const put = useMutation({
    mutationFn: () => save(cert, key),
    onSuccess: () => {
      toast.ok(t("cert.saved"));
      onClose();
    },
    onError: (e) => {
      if (!(e instanceof ApiError && Object.keys(e.fields).length)) toast.error(errorText(e));
    },
  });
  const drop = useMutation({
    mutationFn: clear,
    onSuccess: () => {
      setClearing(false);
      toast.ok(t("cert.cleared"));
      onClose();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const errors = fieldErrors(put.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (cert.trim() && key.trim()) put.mutate();
  };
  const has = !!current && !current.error;
  const left = certDaysLeft(current?.not_after);
  return (
    <Drawer
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={title}
      meta={meta}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="cert-form" loading={put.isPending} disabled={!cert.trim() || !key.trim()}>
            <ShieldCheck size={16} aria-hidden /> {t("cert.install")}
          </Button>
        </>
      }
    >
      <p className="pt-5 text-[13px] text-[var(--ink-600)]">{lead}</p>
      {current ? (
        <div className={`panel-soft mt-4 p-3 ${current.error ? "border-[var(--berry-600)]" : ""}`}>
          {current.error ? (
            <p className="text-[13px] text-[var(--berry-600)]" role="alert">
              {tMaybe(`errors.acme.${current.error}`) ?? current.error}
            </p>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-xs text-[var(--ink-500)]">{t("cert.now")}</span>
                <span className="flex gap-1.5">
                  {current.trusted ? <Pill tone="ok">{t("cert.trusted")}</Pill> : <Pill tone="off">{t("cert.pinned")}</Pill>}
                  {left < 14 ? <Pill tone="warn">{t("cert.soon", { n: Math.max(left, 0) })}</Pill> : null}
                </span>
              </div>
              <div className="mono mt-1 truncate text-[13px]">{current.names?.join(", ")}</div>
              <div className="mt-1 text-xs text-[var(--ink-500)]">{t("cert.issued", { issuer: current.issuer || "—", until: certUntil(current.not_after) })}</div>
            </>
          )}
        </div>
      ) : null}
      <form id="cert-form" onSubmit={submit} noValidate className="pt-5">
        <PemField
          id="cert-chain"
          label={t("cert.chain")}
          hint={t("cert.chainHint")}
          icon={<FileText size={16} aria-hidden />}
          value={cert}
          file={certFile}
          onChange={(v, f) => {
            setCert(v);
            setCertFile(f);
          }}
          placeholder="-----BEGIN CERTIFICATE-----"
          error={errors.cert}
          accept=".pem,.crt,.cer,.txt"
        />
        <PemField
          id="cert-key"
          label={t("cert.key")}
          hint={t("cert.keyHint")}
          icon={<FileKey2 size={16} aria-hidden />}
          value={key}
          file={keyFile}
          onChange={(v, f) => {
            setKey(v);
            setKeyFile(f);
          }}
          placeholder="-----BEGIN PRIVATE KEY-----"
          error={errors.key}
          accept=".pem,.key,.txt"
          secret
        />
        <p className="text-xs text-[var(--ink-500)]">{t("cert.cliHint")}</p>
        <code className="mono mt-2 block overflow-x-auto whitespace-pre rounded-xl bg-[var(--ink-50)] p-3 text-[12px]">{t("cert.cliExample")}</code>
      </form>
      {has ? (
        <div className="mt-5 border-t border-[var(--hairline)] pt-4">
          <Button size="sm" variant="danger" onClick={() => setClearing(true)}>
            {clearLabel}
          </Button>
        </div>
      ) : null}
      <Confirm open={clearing} onOpenChange={setClearing} title={clearLabel} text={clearText} confirm={clearLabel} danger loading={drop.isPending} onConfirm={() => drop.mutate()} />
    </Drawer>
  );
}

// A PEM file picked or pasted. A key loaded from a file stays out of sight.
function PemField({
  id,
  label,
  hint,
  icon,
  value,
  file,
  onChange,
  placeholder,
  error,
  accept,
  secret,
}: {
  id: string;
  label: string;
  hint: string;
  icon: React.ReactNode;
  value: string;
  file: string;
  onChange: (value: string, file: string) => void;
  placeholder: string;
  error?: string;
  accept: string;
  secret?: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [tooBig, setTooBig] = useState(false);
  const pick = async (f: File | undefined) => {
    if (!f) return;
    setTooBig(f.size > MAX);
    if (f.size > MAX) return;
    onChange(await f.text(), f.name);
  };
  const shownError = tooBig ? t("cert.tooBig") : error;
  return (
    <Field label={label} htmlFor={id} hint={hint} error={shownError}>
      <div className="flex flex-col gap-2">
        {file && secret ? (
          <div className="panel-soft flex items-center justify-between gap-2 px-3 py-2 text-[13px]">
            <span className="flex min-w-0 items-center gap-2">
              {icon}
              <span className="mono truncate">{file}</span>
            </span>
            <button type="button" className="text-xs font-medium text-[var(--ink-500)] hover:text-[var(--ink-900)]" onClick={() => onChange("", "")}>
              {t("cert.replace")}
            </button>
          </div>
        ) : (
          <textarea
            id={id}
            className="input mono text-[12px]"
            rows={5}
            value={value}
            onChange={(e) => onChange(e.target.value, "")}
            placeholder={placeholder}
            spellCheck={false}
            autoComplete="off"
            aria-invalid={!!shownError}
          />
        )}
        <div>
          <input ref={input} type="file" accept={accept} className="hidden" onChange={(e) => void pick(e.target.files?.[0])} aria-hidden tabIndex={-1} />
          <Button size="sm" onClick={() => input.current?.click()}>
            <Upload size={16} aria-hidden /> {t("cert.pick")}
          </Button>
        </div>
      </div>
    </Field>
  );
}
