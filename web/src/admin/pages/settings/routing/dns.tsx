import { useState, type ChangeEvent } from "react";
import { Card } from "../../../../components/layout";
import { Field } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { DNS_PRESETS, dnsOfText, dnsTextOf, type DNS, type DnsText, type Routes, type SetRoutes } from "./model";

/**
 * The text of the DNS fields next to the routes they set: the fields keep what is typed (a
 * server half written), the routes get what it stands for. reset puts the text back to dns.
 */
export function useDeviceDns(dns: DNS | undefined, setRoutes: SetRoutes) {
  const [text, setText] = useState(() => dnsTextOf(dns));
  return {
    text,
    onPreset: (next: DNS) => {
      setRoutes((r: Routes) => ({ ...r, dns: next }));
      setText(dnsTextOf(next));
    },
    onText: (next: DnsText) => {
      setText(next);
      setRoutes((r: Routes) => ({ ...r, dns: dnsOfText(next) }));
    },
    reset: (to: DNS | undefined) => setText(dnsTextOf(to)),
  };
}

/** The card of the DNS of the device's profile: a ready set or the servers as mihomo writes
 * them. dns is the fields' state (useDeviceDns). */
export function DeviceDnsCard({ dns }: { dns: ReturnType<typeof useDeviceDns> }) {
  const { text, onPreset, onText } = dns;
  const edit = (k: keyof DnsText) => (e: ChangeEvent<HTMLTextAreaElement>) => onText({ ...text, [k]: e.target.value });
  return (
    <Card title={t("settings.routesDnsDevice")} sub={t("settings.routesDnsDeviceSub")}>
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t("settings.routesDnsPresets")}>
        {DNS_PRESETS.map((p) => (
          <button key={p.id} type="button" className="chip-btn" onClick={() => onPreset(p.dns)}>
            {t(`settings.routesDnsPreset.${p.id}` as "settings.routesDnsPreset.panel")}
          </button>
        ))}
      </div>
      <Field label={t("settings.routesDnsServers")} htmlFor="s-dns-ns" hint={t("settings.routesDnsHint")}>
        <textarea id="s-dns-ns" className="input mono min-h-[72px]" value={text.nameserver} onChange={edit("nameserver")} spellCheck={false} placeholder="https://1.1.1.1/dns-query#PROXY" />
      </Field>
      <Field label={t("settings.routesDnsProxy")} htmlFor="s-dns-proxy" hint={t("settings.routesDnsProxyHint")}>
        <textarea id="s-dns-proxy" className="input mono min-h-[56px]" value={text.proxy} onChange={edit("proxy")} spellCheck={false} placeholder="1.1.1.1" />
      </Field>
      <Field label={t("settings.routesDnsPolicy")} htmlFor="s-dns-policy" hint={t("settings.routesDnsPolicyHint")}>
        <textarea id="s-dns-policy" className="input mono min-h-[72px]" value={text.policy} onChange={edit("policy")} spellCheck={false} placeholder="+.cn: https://dns.alidns.com/dns-query" />
      </Field>
    </Card>
  );
}
