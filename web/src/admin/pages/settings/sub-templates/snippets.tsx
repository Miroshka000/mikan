import { t } from "../../../../i18n";

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

/** Ready pieces of a Clash profile, one tap each: DNS, TUN, sniffing, a QUIC block, a group of a country or of a node. */
export function ClashSnippets({ names, current, onApply }: { names: string[]; current: string; onApply: (text: string) => void }) {
  const snippets = [
    { id: "dns", apply: (v: string) => putSection(v, "dns", DNS_FAKE_IP) },
    { id: "tun", apply: (v: string) => putSection(v, "tun", TUN) },
    { id: "sniffer", apply: (v: string) => putSection(v, "sniffer", SNIFFER) },
    { id: "quic", apply: (v: string) => addToList(v, "rules", "  - AND,((NETWORK,UDP),(DST-PORT,443)),REJECT", true) },
    {
      id: "country",
      apply: (v: string) =>
        addToList(
          v,
          "proxy-groups",
          `  - name: ${t("settings.subTemplates.sample.country")}\n    type: url-test\n    include-all-proxies: true\n    filter: "🇩🇪"\n    url: https://www.gstatic.com/generate_204\n    interval: 300`,
        ),
    },
    { id: "node", apply: (v: string) => addToList(v, "proxy-groups", `  - name: 📺 YouTube\n    type: select\n    mikan:\n      nodes: ["${names[0] ?? "1"}"]`) },
  ] as const;
  return (
    <div className="flex flex-wrap gap-2" role="group" aria-label={t("settings.subTemplates.snippets")}>
      {snippets.map((x) => (
        <button key={x.id} type="button" className="chip-btn" onClick={() => onApply(x.apply(current))}>
          + {t(`settings.subTemplates.snippet.${x.id}`)}
        </button>
      ))}
    </div>
  );
}
