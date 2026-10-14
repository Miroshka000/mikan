import { useMutation } from "@tanstack/react-query";
import { lazy, Suspense, useState, type FormEvent } from "react";
import { ApiError, errorText, type Schemas } from "../../../api/client";
import { useNodes } from "../../../api/hooks";
import { FormActions } from "../../../components/layout";
import { Button, Pill, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { useSaveSettings } from "./shared";

const ConfigEditor = lazy(() => import("../../../components/config-editor"));

/** text with the top-level section key replaced by block, or block added at the end. */
function putSection(text: string, key: string, block: string): string {
  const lines = text.replace(/\s*$/, "").split("\n");
  const start = lines.findIndex((l) => l.startsWith(`${key}:`));
  if (start < 0) return (text.trim() ? text.replace(/\s*$/, "\n\n") : "") + block.trim() + "\n";
  let end = start + 1;
  while (end < lines.length && (lines[end] === "" || /^[\s#]/.test(lines[end]!))) end++;
  return [...lines.slice(0, start), ...block.trim().split("\n"), ...lines.slice(end)].join("\n") + "\n";
}

/** text with item lines added at the end (or, first, at the start) of the top-level list key. */
function addToList(text: string, key: string, item: string, first = false): string {
  const lines = text.replace(/\s*$/, "").split("\n");
  const start = lines.findIndex((l) => l.startsWith(`${key}:`));
  if (start < 0) return (text.trim() ? text.replace(/\s*$/, "\n\n") : "") + `${key}:\n` + item.replace(/\s*$/, "") + "\n";
  let end = start + 1;
  while (end < lines.length && (lines[end] === "" || /^[\s#]/.test(lines[end]!))) end++;
  while (end > start + 1 && lines[end - 1] === "") end--;
  const at = first ? start + 1 : end;
  return [...lines.slice(0, at), ...item.replace(/\s*$/, "").split("\n"), ...lines.slice(at)].join("\n") + "\n";
}

const DNS_FAKE_IP = `dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter:
    - "*.lan"
    - "+.local"
    - "+.ntp.org"
    - "+.msftconnecttest.com"
  default-nameserver:
    - 1.1.1.1
    - 77.88.8.8
  nameserver:
    - https://1.1.1.1/dns-query#PROXY
    - https://8.8.8.8/dns-query#PROXY
  proxy-server-nameserver:
    - 1.1.1.1
    - 77.88.8.8
  nameserver-policy:
    "+.ru": [https://77.88.8.8/dns-query]
    "+.su": [https://77.88.8.8/dns-query]
    "+.xn--p1ai": [https://77.88.8.8/dns-query]`;

const TUN = `tun:
  enable: true
  stack: mixed
  auto-route: true
  auto-detect-interface: true
  strict-route: true
  dns-hijack:
    - any:53`;

const SNIFFER = `sniffer:
  enable: true
  parse-pure-ip: true
  override-destination: true
  sniff:
    HTTP:
      ports: [80, 8080-8880]
    TLS:
      ports: [443, 8443]
    QUIC:
      ports: [443, 8443]`;

/** The admin's own Clash profile: a mihomo config the panel puts the user's servers into. */
export function TemplateCard({ s, text, setText, starter }: { s: Schemas["SettingsView"]; text: string; setText: (f: (v: string) => string) => void; starter: () => Promise<string> }) {
  const save = useSaveSettings();
  const nodes = useNodes();
  const [sure, setSure] = useState(false);
  const fromSimple = useMutation({ mutationFn: starter, onSuccess: (yaml) => setText(() => yaml) });
  const apiErr = save.error instanceof ApiError ? save.error : null;
  const error = apiErr?.fields.sub_template;
  const live = s.sub_template.trim() !== "";
  const dirty = text !== s.sub_template;
  const names = (nodes.data ?? []).map((n) => n.name || String(n.id));
  const snippets = [
    { id: "dns", apply: (v: string) => putSection(v, "dns", DNS_FAKE_IP) },
    { id: "tun", apply: (v: string) => putSection(v, "tun", TUN) },
    { id: "sniffer", apply: (v: string) => putSection(v, "sniffer", SNIFFER) },
    { id: "quic", apply: (v: string) => addToList(v, "rules", "  - AND,((NETWORK,UDP),(DST-PORT,443)),REJECT", true) },
    {
      id: "country",
      apply: (v: string) => addToList(v, "proxy-groups", `  - name: 🇩🇪 Германия\n    type: url-test\n    include-all-proxies: true\n    filter: "🇩🇪"\n    url: https://www.gstatic.com/generate_204\n    interval: 300`),
    },
    {
      id: "node",
      apply: (v: string) => addToList(v, "proxy-groups", `  - name: 📺 YouTube\n    type: select\n    mikan:\n      nodes: ["${names[0] ?? "1"}"]`),
    },
  ] as const;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ sub_template: text });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.template")}</h2>
            <div className="card-sub">{t("settings.templateSub")}</div>
          </div>
          <Pill tone={live ? "ok" : "off"}>{live ? t("settings.templateLive") : t("settings.templateOff")}</Pill>
        </div>
        {!live ? <div className="banner mb-4">{t("settings.templateNotLive")}</div> : null}
        <div className="mb-3 flex flex-wrap gap-2">
          <Button variant="ghost" loading={fromSimple.isPending} onClick={() => fromSimple.mutate()}>
            {t("settings.templateFromSimple")}
          </Button>
        </div>
        {fromSimple.error ? (
          <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
            {errorText(fromSimple.error)}
          </p>
        ) : null}
        <div className="mb-3 flex flex-wrap gap-2" role="group" aria-label={t("settings.templateSnippets")}>
          {snippets.map((x) => (
            <button key={x.id} type="button" className="chip-btn" onClick={() => setText(x.apply)}>
              + {t(`settings.templateSnippet.${x.id}` as "settings.templateSnippet.dns")}
            </button>
          ))}
        </div>
        <Suspense fallback={<Skeleton style={{ height: 420, borderRadius: 12 }} />}>
          <ConfigEditor kind="profile" value={text} onChange={(v) => setText(() => v)} label={t("settings.template")} invalid={!!error} />
        </Suspense>
        {error ? (
          <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
        <details className="mt-3 text-xs text-[var(--ink-500)]">
          <summary className="cursor-pointer font-medium text-[var(--ink-700)]">{t("settings.templateHow")}</summary>
          <p className="mt-2">{t("settings.templateHowServers")}</p>
          <pre className="panel-soft mono mt-2 overflow-auto p-2 text-[12px]">{`- name: 🇩🇪 Германия
  type: url-test
  include-all-proxies: true
  filter: "🇩🇪"

- name: 🔒 Быстрые
  type: url-test
  mikan:
    nodes: [${names.slice(0, 2).map((n) => `"${n}"`).join(", ") || '"1"'}]
    types: [vless, hysteria2]`}</pre>
          <p className="mt-2">{t("settings.templateHowNodes")}</p>
          <div className="mt-1 flex flex-wrap gap-1.5">
            {names.map((n) => (
              <code key={n} className="tag mono">
                {n}
              </code>
            ))}
          </div>
          <p className="mt-2">{t("settings.templateHowRest")}</p>
        </details>
        <FormActions dirty={dirty} saving={save.isPending && save.variables?.sub_template === text} disabled={!dirty || !text.trim()} onReset={live ? () => setText(() => s.sub_template) : undefined}>
          {live ? (
            <Button
              variant="ghost"
              loading={save.isPending && save.variables?.sub_template === ""}
              onClick={() => {
                if (!sure) return setSure(true);
                setSure(false);
                save.mutate({ sub_template: "" });
              }}
            >
              {sure ? t("settings.templateOffSure") : t("settings.templateTurnOff")}
            </Button>
          ) : null}
        </FormActions>
      </form>
    </section>
  );
}
