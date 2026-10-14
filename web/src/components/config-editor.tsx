// YAML editor for listener templates and for the own Clash profile. Loaded lazily:
// CodeMirror is only needed when an admin opens one of them.
import { autocompletion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { EditorState } from "@codemirror/state";
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from "@codemirror/view";
import { useEffect, useRef } from "react";
import { FINGERPRINTS } from "../lib/fingerprints";

// Mirrors the allow list in internal/proto (rules): the server is the authority, this only
// saves typing.
const TOP: Record<string, string[]> = {
  vless: ["ws-path", "grpc-service-name", "xhttp-config", "reality-config", "mux-option", "decryption"],
  vmess: ["ws-path", "grpc-service-name", "reality-config", "mux-option"],
  trojan: ["ws-path", "grpc-service-name", "reality-config", "mux-option"],
  hysteria2: ["obfs", "obfs-password", "obfs-min-packet-size", "obfs-max-packet-size", "alpn", "up", "down", "ignore-client-bandwidth", "masquerade", "max-idle-time", "cwnd", "udp-mtu", "bbr-profile"],
  tuic: ["congestion-controller", "alpn", "max-idle-time", "authentication-timeout", "max-udp-relay-packet-size", "cwnd", "bbr-profile"],
  anytls: ["padding-scheme"],
  trusttunnel: ["congestion-controller", "cwnd", "bbr-profile"],
  shadowquic: ["jls-upstream", "alpn", "quic-versions", "congestion-controller", "up", "down", "max-idle-time", "cwnd", "bbr-profile"],
  mieru: ["transport"],
  shadowsocks: ["cipher", "password"],
  sudoku: ["key", "aead-method", "padding-min", "padding-max", "table-type", "httpmask"],
  snell: ["psk", "version", "obfs-opts"],
};
const NESTED: Record<string, string[]> = {
  "reality-config": ["dest", "private-key", "short-id", "server-names", "max-time-difference", "limit-fallback-upload", "limit-fallback-download"],
  "xhttp-config": ["path", "mode", "host", "x-padding-bytes"],
  mikan: ["flow", "tls", "client"],
  client: ["server", "port", "sni", "fingerprint"],
  "jls-upstream": ["addr", "sni"],
  httpmask: ["disable", "mode", "path-root"],
  "obfs-opts": ["mode", "host"],
};
const VALUES: Record<string, string[]> = {
  type: Object.keys(TOP),
  mode: ["stream-one", "stream-up", "packet-up"],
  tls: ["node"],
  flow: ["xtls-rprx-vision"],
  obfs: ["salamander", "gecko"],
  "congestion-controller": ["bbr", "cubic", "new_reno"],
  cipher: ["2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"],
  transport: ["TCP"],
  "aead-method": ["chacha20-poly1305", "aes-128-gcm"],
  "table-type": ["prefer_ascii", "prefer_entropy", "up_ascii_down_entropy", "up_entropy_down_ascii"],
  fingerprint: [...FINGERPRINTS],
};

const indent = (s: string) => s.length - s.trimStart().length;

/** The key whose block contains line n, or "" at the top level. */
function parentKey(doc: EditorState["doc"], lineNo: number, own: number): string {
  for (let n = lineNo - 1; n >= 1; n--) {
    const text = doc.line(n).text;
    if (!text.trim() || text.trimStart().startsWith("#")) continue;
    if (indent(text) < own) {
      const m = /^\s*([\w-]+):\s*$/.exec(text);
      return m ? m[1]! : "";
    }
  }
  return "";
}

// The own Clash profile: mihomo's keys an admin types most, and mikan's for the groups.
const PROFILE_TOP = ["mixed-port", "allow-lan", "mode", "log-level", "ipv6", "unified-delay", "tcp-concurrent", "find-process-mode", "geodata-mode", "geo-auto-update", "geox-url", "profile", "hosts", "dns", "tun", "sniffer", "proxies", "proxy-groups", "proxy-providers", "rule-providers", "sub-rules", "rules"];
const PROFILE_NESTED: Record<string, string[]> = {
  dns: ["enable", "listen", "ipv6", "enhanced-mode", "fake-ip-range", "fake-ip-filter", "respect-rules", "use-hosts", "use-system-hosts", "default-nameserver", "nameserver", "proxy-server-nameserver", "direct-nameserver", "direct-nameserver-follow-policy", "nameserver-policy", "fallback"],
  tun: ["enable", "stack", "auto-route", "auto-detect-interface", "strict-route", "dns-hijack", "mtu", "route-exclude-address"],
  sniffer: ["enable", "parse-pure-ip", "force-dns-mapping", "override-destination", "sniff", "skip-domain", "skip-dst-address"],
  profile: ["store-selected", "store-fake-ip"],
  "proxy-groups": ["name", "type", "proxies", "include-all-proxies", "filter", "exclude-filter", "exclude-type", "mikan", "url", "interval", "tolerance", "timeout", "lazy", "hidden", "icon", "strategy", "max-failed-times", "expected-status"],
  mikan: ["nodes", "types"],
  "rule-providers": [],
};
const PROFILE_VALUES: Record<string, string[]> = {
  type: ["select", "url-test", "fallback", "load-balance", "relay", "http"],
  mode: ["rule", "global", "direct"],
  "log-level": ["silent", "error", "warning", "info", "debug"],
  "enhanced-mode": ["fake-ip", "redir-host"],
  stack: ["system", "gvisor", "mixed"],
  strategy: ["consistent-hashing", "round-robin", "sticky-sessions"],
  behavior: ["domain", "ipcidr", "classical"],
  format: ["yaml", "text", "mrs"],
  "find-process-mode": ["strict", "always", "off"],
};

/** The key whose block holds a key at column own on line n: a list item ("- name: x") passes on to its list's key. */
function profileParent(doc: EditorState["doc"], lineNo: number, own: number): string {
  if (own === 0) return "";
  let col = own;
  for (let n = lineNo - 1; n >= 1; n--) {
    const text = doc.line(n).text;
    if (!text.trim() || text.trimStart().startsWith("#") || indent(text) >= col) continue;
    const m = /^\s*([\w-]+):\s*$/.exec(text);
    if (m) return m[1]!;
    if (/^\s*-\s/.test(text)) {
      col = indent(text) + 1; // the list item: look for the key of its list
      continue;
    }
    return "";
  }
  return "";
}

function completeProfile(ctx: CompletionContext): CompletionResult | null {
  const line = ctx.state.doc.lineAt(ctx.pos);
  const before = line.text.slice(0, ctx.pos - line.from);
  const value = /^\s*(-\s+)?([\w-]+):\s*([\w-]*)$/.exec(before);
  if (value) {
    const options = PROFILE_VALUES[value[2]!];
    if (!options) return null;
    return { from: ctx.pos - value[3]!.length, options: options.map((v) => ({ label: v, type: "constant" })) };
  }
  const key = /^(\s*(?:-\s+)?)([\w-]*)$/.exec(before);
  if (!key || (!ctx.explicit && key[2] === "")) return null;
  const parent = profileParent(ctx.state.doc, line.number, key[1]!.length);
  const keys = parent === "" ? PROFILE_TOP : (PROFILE_NESTED[parent] ?? []);
  return { from: ctx.pos - key[2]!.length, options: keys.map((k) => ({ label: k, type: "property", apply: `${k}: ` })) };
}

function complete(ctx: CompletionContext): CompletionResult | null {
  const line = ctx.state.doc.lineAt(ctx.pos);
  const before = line.text.slice(0, ctx.pos - line.from);
  const value = /^\s*(-\s+)?([\w-]+):\s*([\w-]*)$/.exec(before);
  if (value) {
    const options = VALUES[value[2]!];
    if (!options) return null;
    return { from: ctx.pos - value[3]!.length, options: options.map((v) => ({ label: v, type: "constant" })) };
  }
  const key = /^(\s*)([\w-]*)$/.exec(before);
  if (!key || (!ctx.explicit && key[2] === "")) return null;
  const parent = parentKey(ctx.state.doc, line.number, key[1]!.length);
  let keys: string[];
  if (parent === "") {
    const type = /^type:\s*([\w-]+)/m.exec(ctx.state.doc.toString())?.[1] ?? "";
    keys = ["type", "mikan", ...(TOP[type] ?? Object.values(TOP).flat())];
  } else {
    keys = NESTED[parent] ?? [];
  }
  return { from: ctx.pos - key[2]!.length, options: [...new Set(keys)].map((k) => ({ label: k, type: "property", apply: `${k}: ` })) };
}

// YAML in the theme's own inks: CodeMirror's default colours are made for a white page and
// sink into a dark one.
const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: "var(--mikan-700)" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--leaf-700)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--lagoon-600)" },
  { tag: [tags.keyword, tags.typeName, tags.labelName], color: "var(--fc-purple)" },
  { tag: tags.comment, color: "var(--ink-400)", fontStyle: "italic" },
  { tag: [tags.punctuation, tags.separator, tags.bracket, tags.operator], color: "var(--ink-500)" },
  { tag: tags.invalid, color: "var(--berry-600)" },
]);

const theme = EditorView.theme({
  "&": { fontSize: "13px", backgroundColor: "transparent" },
  ".cm-content": { fontFamily: "var(--font-mono-stack)", padding: "8px 0" },
  ".cm-content, .cm-line": { color: "var(--ink-900)", caretColor: "var(--ink-900)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--ink-900)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "color-mix(in srgb, var(--mikan-500) 24%, transparent)" },
  ".cm-gutters": { backgroundColor: "transparent", border: "none", color: "var(--ink-400)" },
  ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--hover)" },
  "&.cm-focused": { outline: "none" },
  ".cm-tooltip": { backgroundColor: "var(--surface-solid)", color: "var(--ink-900)", border: "1px solid var(--hairline)", boxShadow: "var(--shadow-md)" },
  ".cm-tooltip-autocomplete": { borderRadius: "12px", overflow: "hidden" },
  ".cm-tooltip-autocomplete ul li[aria-selected]": { backgroundColor: "var(--mikan-50)", color: "var(--ink-900)" },
});

export default function ConfigEditor({ value, onChange, label, invalid, kind = "inbound" }: { value: string; onChange: (v: string) => void; label: string; invalid?: boolean; kind?: "inbound" | "profile" }) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const change = useRef(onChange);
  change.current = onChange;

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          // What a short YAML template needs. The "codemirror" package's basicSetup also brings
          // search, lint, folding, bracket matching and more, and binds keys (Ctrl+F,
          // Ctrl+Shift+M) the form has no use for.
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightActiveLine(),
          history(),
          drawSelection(),
          indentOnInput(),
          syntaxHighlighting(highlight),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          yaml(),
          autocompletion({ override: [kind === "profile" ? completeProfile : complete] }),
          theme,
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": label, spellcheck: "false" }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) change.current(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => v.destroy();
    // The editor owns the text after mount; outside resets go through the effect below.
  }, []);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
  }, [value]);

  return <div ref={host} className="config-editor" aria-invalid={invalid || undefined} />;
}
