import { TriangleAlert } from "lucide-react";
import type { Schemas } from "../../../api/client";
import { Field, Segmented } from "../../../components/ui";
import { Switch } from "../../../components/switch";
import { t } from "../../../i18n";

export type XHTTP = Schemas["XHTTPTuning"];

export const XHTTP_DEFAULT: XHTTP = {
  mode: "",
  x_padding_bytes: "",
  x_padding_obfs_mode: false,
  x_padding_placement: "",
  x_padding_key: "",
  x_padding_header: "",
  x_padding_method: "",
  uplink_http_method: "",
  uplink_chunk_size: "",
  sc_max_each_post_bytes: "",
  xmux_max_concurrency: "",
  xmux_max_connections: "",
  xmux_c_max_reuse_times: "",
  xmux_h_max_request_times: "",
  xmux_h_max_reusable_secs: "",
  sc_min_posts_interval_ms: "",
  session_placement: "",
  session_key: "",
  seq_placement: "",
  seq_key: "",
  uplink_data_placement: "",
  uplink_data_key: "",
  sc_stream_up_server_secs: "",
  sc_max_buffered_posts: "",
  no_sse_header: false,
  session_table: "",
  session_length: "",
};

/** The names both cores use when a placement has no key of its own. */
const KEY_HINT = { header: "X-Session", cookie: "x_session", query: "x_session" } as const;
const SEQ_KEY_HINT = { header: "X-Seq", cookie: "x_seq", query: "x_seq" } as const;
const SESSION_TABLES = ["hex", "base36", "Base62", "number", "alphabet", "Alphabet", "HEX", "BASE36", "ALPHABET"];

/** A short random token for the padding's key: a fixed one would be a mark of its own. */
function token() {
  const abc = "abcdefghijklmnopqrstuvwxyz";
  const a = new Uint8Array(6);
  crypto.getRandomValues(a);
  return Array.from(a, (x) => abc[x % abc.length]).join("");
}

type Preset = "standard" | "noise" | "max";

// Ready sets: what most admins want without reading the Xray docs. "max" changes how the
// requests look on the wire, so older apps lose the inbound: the form says so.
function preset(p: Preset): XHTTP {
  switch (p) {
    case "noise":
      return { ...XHTTP_DEFAULT, x_padding_bytes: "500-3000", xmux_max_concurrency: "8-16", xmux_h_max_reusable_secs: "900-1800" };
    case "max":
      return {
        ...XHTTP_DEFAULT,
        x_padding_bytes: "1000-4000",
        x_padding_obfs_mode: true,
        x_padding_placement: "cookie",
        x_padding_key: token(),
        x_padding_method: "tokenish",
        uplink_http_method: "PUT",
        xmux_max_concurrency: "4-8",
        xmux_h_max_reusable_secs: "600-1200",
      };
    default:
      return { ...XHTTP_DEFAULT };
  }
}

function presetOf(x: XHTTP): Preset | "own" {
  const same = (a: XHTTP, b: XHTTP) => (Object.keys(a) as (keyof XHTTP)[]).every((k) => k === "x_padding_key" || a[k] === b[k]);
  if (same(x, XHTTP_DEFAULT)) return "standard";
  if (same(x, preset("noise"))) return "noise";
  if (same(x, preset("max"))) return "max";
  return "own";
}

/** The XHTTP tab: the server's settings, which reach the apps, and the apps' own reuse of
 * connections. An empty field is the core's default. */
export function XhttpTuning({ value, onChange, error }: { value: XHTTP; onChange: (x: XHTTP) => void; error?: string }) {
  const set = (k: keyof XHTTP) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => onChange({ ...value, [k]: e.target.value });
  const current = presetOf(value);
  // A placement other than the default moves things where an older app does not look.
  const strong =
    value.x_padding_obfs_mode ||
    value.uplink_http_method !== "" ||
    !["", "path"].includes(value.session_placement ?? "") ||
    !["", "path"].includes(value.seq_placement ?? "") ||
    !["", "body"].includes(value.uplink_data_placement ?? "");
  const input = (k: keyof XHTTP, placeholder: string, label: string, hint?: string, numeric = true, list?: string) => (
    <Field label={label} htmlFor={`x-${k}`} hint={hint}>
      <input id={`x-${k}`} className="input mono" value={(value[k] as string) ?? ""} onChange={set(k)} placeholder={placeholder} inputMode={numeric ? "numeric" : undefined} list={list} autoComplete="off" spellCheck={false} />
    </Field>
  );
  const placement = (k: "session_placement" | "seq_placement", label: string) => (
    <Field label={label} htmlFor={`x-${k}`}>
      <select id={`x-${k}`} className="input" value={value[k] ?? ""} onChange={set(k)}>
        <option value="">path</option>
        <option value="query">query</option>
        <option value="header">header</option>
        <option value="cookie">cookie</option>
      </select>
    </Field>
  );
  // header and cookie carry the upload only in packet-up and only under a name.
  const setData = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const v = e.target.value as XHTTP["uplink_data_placement"];
    const named = v === "header" || v === "cookie";
    onChange({
      ...value,
      uplink_data_placement: v,
      mode: named ? "packet-up" : value.mode,
      uplink_data_key: named && !value.uplink_data_key ? (v === "header" ? "X-Data" : "x_data") : value.uplink_data_key,
    });
  };
  return (
    <div className="flex flex-col gap-1">
      <p className="mb-2 text-[13px] text-[var(--ink-500)]">{t("inbounds.xhttp.intro")}</p>
      <Field label={t("inbounds.xhttp.preset")}>
        <div className="flex flex-wrap gap-2" role="group" aria-label={t("inbounds.xhttp.preset")}>
          {(["standard", "noise", "max"] as Preset[]).map((p) => (
            <button key={p} type="button" className="chip-btn" aria-pressed={current === p} onClick={() => onChange(preset(p))}>
              {t(`inbounds.xhttp.presets.${p}`)}
            </button>
          ))}
          {current === "own" ? <span className="self-center text-xs text-[var(--ink-500)]">{t("inbounds.xhttp.presets.own")}</span> : null}
        </div>
      </Field>
      {strong ? (
        <div className="mb-3 flex items-start gap-2 rounded-xl border border-[var(--hairline)] bg-[var(--honey-50)] p-3 text-xs text-[var(--honey-600)]" role="note">
          <TriangleAlert size={16} className="mt-px shrink-0" aria-hidden />
          <span>{t("inbounds.xhttp.strongWarn")}</span>
        </div>
      ) : null}
      {error ? (
        <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}

      <Field label={t("inbounds.xhttp.mode")} hint={t("inbounds.xhttp.modeHint")}>
        <Segmented
          label={t("inbounds.xhttp.mode")}
          value={value.mode || "default"}
          onChange={(v) => onChange({ ...value, mode: v === "default" ? "" : v })}
          options={[
            { value: "default", label: t("inbounds.xhttp.byDefault") },
            { value: "stream-one", label: "stream-one" },
            { value: "stream-up", label: "stream-up" },
            { value: "packet-up", label: "packet-up" },
          ]}
        />
      </Field>

      <h3 className="mt-3 mb-1 text-[13px] font-semibold">{t("inbounds.xhttp.padding")}</h3>
      {input("x_padding_bytes", "100-1000", t("inbounds.xhttp.paddingBytes"), t("inbounds.xhttp.rangeHint"))}
      <div className="flex items-start justify-between gap-4 py-2">
        <div className="min-w-0">
          <div className="text-[13px] font-medium">{t("inbounds.xhttp.obfs")}</div>
          <div className="mt-1 text-xs text-[var(--ink-500)]">{t("inbounds.xhttp.obfsHint")}</div>
        </div>
        <Switch
          checked={!!value.x_padding_obfs_mode}
          label={t("inbounds.xhttp.obfs")}
          onChange={(v) => onChange({ ...value, x_padding_obfs_mode: v, x_padding_key: v && !value.x_padding_key ? token() : value.x_padding_key })}
        />
      </div>
      {value.x_padding_obfs_mode ? (
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("inbounds.xhttp.placement")} htmlFor="x-placement">
            <select id="x-placement" className="input" value={value.x_padding_placement} onChange={set("x_padding_placement")}>
              <option value="">{t("inbounds.xhttp.byDefault")}</option>
              <option value="cookie">cookie</option>
              <option value="header">header</option>
              <option value="query">query</option>
              <option value="queryInHeader">queryInHeader</option>
            </select>
          </Field>
          <Field label={t("inbounds.xhttp.method")} htmlFor="x-method">
            <select id="x-method" className="input" value={value.x_padding_method} onChange={set("x_padding_method")}>
              <option value="">{t("inbounds.xhttp.byDefault")}</option>
              <option value="repeat-x">repeat-x</option>
              <option value="tokenish">tokenish</option>
            </select>
          </Field>
          {input("x_padding_key", "x_padding", t("inbounds.xhttp.key"))}
          {input("x_padding_header", "Referer", t("inbounds.xhttp.header"))}
        </div>
      ) : null}

      <h3 className="mt-3 mb-1 text-[13px] font-semibold">{t("inbounds.xhttp.upload")}</h3>
      <div className="grid gap-x-3 sm:grid-cols-3">
        <Field label={t("inbounds.xhttp.uplinkMethod")} htmlFor="x-uplink">
          <select id="x-uplink" className="input" value={value.uplink_http_method} onChange={set("uplink_http_method")}>
            <option value="">POST</option>
            <option value="PUT">PUT</option>
            <option value="PATCH">PATCH</option>
          </select>
        </Field>
        {input("uplink_chunk_size", "", t("inbounds.xhttp.chunk"))}
        {input("sc_max_each_post_bytes", "1000000", t("inbounds.xhttp.maxPost"))}
      </div>

      <h3 className="mt-3 mb-1 text-[13px] font-semibold">{t("inbounds.xhttp.sessionGroup")}</h3>
      <p className="mb-2 text-xs text-[var(--ink-500)]">{t("inbounds.xhttp.sessionIntro")}</p>
      <div className="grid gap-x-3 sm:grid-cols-2">
        {placement("session_placement", t("inbounds.xhttp.sessionPlace"))}
        {value.session_placement && value.session_placement !== "path"
          ? input("session_key", KEY_HINT[value.session_placement as keyof typeof KEY_HINT] ?? "", t("inbounds.xhttp.key"), undefined, false)
          : null}
        {placement("seq_placement", t("inbounds.xhttp.seqPlace"))}
        {value.seq_placement && value.seq_placement !== "path"
          ? input("seq_key", SEQ_KEY_HINT[value.seq_placement as keyof typeof SEQ_KEY_HINT] ?? "", t("inbounds.xhttp.key"), undefined, false)
          : null}
        <Field label={t("inbounds.xhttp.dataPlace")} htmlFor="x-uplink_data_placement" hint={t("inbounds.xhttp.dataHint")}>
          <select id="x-uplink_data_placement" className="input" value={value.uplink_data_placement ?? ""} onChange={setData}>
            <option value="">body</option>
            <option value="header">header</option>
            <option value="cookie">cookie</option>
            <option value="auto">auto</option>
          </select>
        </Field>
        {value.uplink_data_placement === "header" || value.uplink_data_placement === "cookie"
          ? input("uplink_data_key", value.uplink_data_placement === "header" ? "X-Data" : "x_data", t("inbounds.xhttp.key"), undefined, false)
          : null}
        {input("session_table", "hex", t("inbounds.xhttp.idTable"), t("inbounds.xhttp.idTableHint"), false, "x-session-tables")}
        {value.session_table ? input("session_length", "16-32", t("inbounds.xhttp.idLength"), t("inbounds.xhttp.idLengthHint")) : null}
      </div>
      <datalist id="x-session-tables">
        {SESSION_TABLES.map((n) => (
          <option key={n} value={n} />
        ))}
      </datalist>

      <h3 className="mt-3 mb-1 text-[13px] font-semibold">{t("inbounds.xhttp.serverGroup")}</h3>
      <div className="grid gap-x-3 sm:grid-cols-2">
        {input("sc_stream_up_server_secs", "20-80", t("inbounds.xhttp.serverSecs"), t("inbounds.xhttp.serverSecsHint"))}
        {input("sc_max_buffered_posts", "30", t("inbounds.xhttp.bufferedPosts"), t("inbounds.xhttp.bufferedPostsHint"))}
      </div>
      <div className="flex items-start justify-between gap-4 py-2">
        <div className="min-w-0">
          <div className="text-[13px] font-medium">{t("inbounds.xhttp.noSse")}</div>
          <div className="mt-1 text-xs text-[var(--ink-500)]">{t("inbounds.xhttp.noSseHint")}</div>
        </div>
        <Switch checked={!!value.no_sse_header} label={t("inbounds.xhttp.noSse")} onChange={(v) => onChange({ ...value, no_sse_header: v })} />
      </div>

      <h3 className="mt-3 mb-1 text-[13px] font-semibold">{t("inbounds.xhttp.reuse")}</h3>
      <p className="mb-2 text-xs text-[var(--ink-500)]">{t("inbounds.xhttp.reuseHint")}</p>
      <div className="grid gap-x-3 sm:grid-cols-2">
        {input("xmux_max_concurrency", "16-32", t("inbounds.xhttp.concurrency"))}
        {input("xmux_max_connections", "", t("inbounds.xhttp.connections"), t("inbounds.xhttp.connectionsHint"))}
        {input("xmux_c_max_reuse_times", "", t("inbounds.xhttp.reuseTimes"))}
        {input("xmux_h_max_request_times", "600-900", t("inbounds.xhttp.requestTimes"))}
        {input("xmux_h_max_reusable_secs", "1800-3000", t("inbounds.xhttp.reusableSecs"))}
        {input("sc_min_posts_interval_ms", "30", t("inbounds.xhttp.postInterval"))}
      </div>
    </div>
  );
}
