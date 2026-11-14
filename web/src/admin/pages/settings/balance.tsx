import type { Schemas } from "../../../api/client";
import { Card } from "../../../components/layout";
import { Select } from "../../../components/select";
import { Field } from "../../../components/ui";
import { t } from "../../../i18n";

type Servers = NonNullable<Schemas["Routes"]["servers"]>;
type Mihomo = NonNullable<Servers["mihomo"]>;
type SingBox = NonNullable<Servers["singbox"]>;
type Xray = NonNullable<Servers["xray"]>;

// The intervals offered; 0 keeps the common one, and one saved otherwise stays offered.
const INTERVALS = [0, 60, 120, 300, 600, 1800];

const MIHOMO_TYPES = [
  { value: "", label: "settings.routesBalanceCommon" },
  { value: "url-test", label: "settings.routesAutoFastest" },
  { value: "fallback", label: "settings.routesAutoFallback" },
  { value: "load-balance", label: "settings.routesBalanceSpread" },
] as const;

const XRAY_STRATEGIES = [
  { value: "", label: "settings.routesXrayByGroup" },
  { value: "leastPing", label: "settings.routesXrayLeastPing" },
  { value: "leastLoad", label: "settings.routesXrayLeastLoad" },
  { value: "random", label: "settings.routesXrayRandom" },
  { value: "roundRobin", label: "settings.routesXrayRoundRobin" },
] as const;

/** What only leastLoad reads: the server refuses it with another strategy. */
const LEAST_LOAD_ONLY = { sampling: undefined, tolerance: undefined, expected: undefined, max_rtt: undefined };

/** Each core's own over the common server choice: an empty one is left out, as the server leaves it. */
export function tidyServers(s: Servers): Servers {
  const out = { ...s };
  for (const k of ["mihomo", "singbox", "xray"] as const) {
    const core = out[k] as Record<string, unknown> | undefined;
    if (core && !Object.values(core).some((v) => v !== undefined && v !== "" && v !== 0)) delete out[k];
  }
  return out;
}

function IntervalField({ id, value, onChange }: { id: string; value?: number; onChange: (n: number | undefined) => void }) {
  return (
    <Field label={t("settings.routesInterval")} htmlFor={id}>
      <Select id={id} className="input max-w-[280px]" value={value ?? 0} onChange={(e) => onChange(Number(e.target.value) || undefined)}>
        {[...new Set([...INTERVALS, value ?? 0])].map((n) => (
          <option key={n} value={n}>
            {n ? t("settings.routesIntervalSec", { n }) : t("settings.routesBalanceCommon")}
          </option>
        ))}
      </Select>
    </Field>
  );
}

/** A number the admin may leave empty for the default; 0 and what does not read are empty. */
function NumberField(props: { id: string; label: string; hint?: string; value?: number; min: number; max: number; step?: number; placeholder?: string; onChange: (n: number | undefined) => void }) {
  const { id, label, hint, value, min, max, step, placeholder, onChange } = props;
  return (
    <Field label={label} htmlFor={id} hint={hint}>
      <input
        id={id}
        className="input max-w-[200px]"
        type="number"
        inputMode="numeric"
        min={min}
        max={max}
        step={step ?? 1}
        placeholder={placeholder}
        value={value ?? ""}
        onChange={(e) => {
          const n = e.target.valueAsNumber;
          onChange(Number.isFinite(n) && n > 0 ? n : undefined);
        }}
      />
    </Field>
  );
}

/** Each core's own balance over the common server choice: what one core's apps can do and the others cannot. */
export function CoreBalance({ servers, setServers, error }: { servers: Servers; setServers: (patch: Partial<Servers>) => void; error?: string }) {
  const m: Mihomo = servers.mihomo ?? {};
  const s: SingBox = servers.singbox ?? {};
  const x: Xray = servers.xray ?? {};
  const setM = (p: Partial<Mihomo>) => setServers({ mihomo: { ...m, ...p } });
  const setS = (p: Partial<SingBox>) => setServers({ singbox: { ...s, ...p } });
  const setX = (p: Partial<Xray>) => setServers({ xray: { ...x, ...p } });
  const leastLoad = x.strategy === "leastLoad";
  return (
    <Card title={t("settings.routesCores")} sub={t("settings.routesCoresSub")} i={2}>
      <div className="flex flex-col gap-6">
        <section className="flex flex-col gap-4" aria-labelledby="b-mihomo">
          <h3 id="b-mihomo" className="text-[13px] font-semibold text-[var(--ink-700)]">
            {t("settings.routesCoreMihomo")}
          </h3>
          <Field label={t("settings.routesAutoKind")} htmlFor="b-mihomo-type">
            <Select id="b-mihomo-type" className="input max-w-[280px]" value={m.type ?? ""} onChange={(e) => setM({ type: (e.target.value || undefined) as Mihomo["type"] })}>
              {MIHOMO_TYPES.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.label)}
                </option>
              ))}
            </Select>
          </Field>
          <IntervalField id="b-mihomo-interval" value={m.interval} onChange={(interval) => setM({ interval })} />
          <NumberField
            id="b-mihomo-tolerance"
            label={t("settings.routesTolerance")}
            hint={t("settings.routesToleranceHint")}
            min={0}
            max={5000}
            step={10}
            placeholder="50"
            value={m.tolerance}
            onChange={(tolerance) => setM({ tolerance })}
          />
        </section>
        <section className="flex flex-col gap-4" aria-labelledby="b-singbox">
          <h3 id="b-singbox" className="text-[13px] font-semibold text-[var(--ink-700)]">
            {t("settings.routesCoreSingBox")}
          </h3>
          <IntervalField id="b-singbox-interval" value={s.interval} onChange={(interval) => setS({ interval })} />
          <NumberField
            id="b-singbox-tolerance"
            label={t("settings.routesTolerance")}
            hint={t("settings.routesToleranceHint")}
            min={0}
            max={5000}
            step={10}
            placeholder="50"
            value={s.tolerance}
            onChange={(tolerance) => setS({ tolerance })}
          />
        </section>
        <section className="flex flex-col gap-4" aria-labelledby="b-xray">
          <h3 id="b-xray" className="text-[13px] font-semibold text-[var(--ink-700)]">
            {t("settings.routesCoreXray")}
          </h3>
          <Field label={t("settings.routesXrayStrategy")} htmlFor="b-xray-strategy" hint={t(leastLoad ? "settings.routesXrayLeastLoadHint" : "settings.routesXrayStrategyHint")}>
            <Select
              id="b-xray-strategy"
              className="input max-w-[280px]"
              value={x.strategy ?? ""}
              onChange={(e) => {
                const strategy = (e.target.value || undefined) as Xray["strategy"];
                setX(strategy === "leastLoad" ? { strategy } : { strategy, ...LEAST_LOAD_ONLY });
              }}
            >
              {XRAY_STRATEGIES.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.label)}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("settings.routesXrayProbe")} htmlFor="b-xray-probe" hint={t("settings.routesXrayProbeHint")}>
            <input
              id="b-xray-probe"
              className="input mono"
              type="url"
              inputMode="url"
              maxLength={500}
              spellCheck={false}
              placeholder="https://www.gstatic.com/generate_204"
              value={x.probe_url ?? ""}
              onChange={(e) => setX({ probe_url: e.target.value.trim() || undefined })}
            />
          </Field>
          <IntervalField id="b-xray-interval" value={x.interval} onChange={(interval) => setX({ interval })} />
          {leastLoad ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <NumberField id="b-xray-expected" label={t("settings.routesXrayExpected")} min={1} max={100} placeholder="1" value={x.expected} onChange={(expected) => setX({ expected })} />
              <NumberField id="b-xray-maxrtt" label={t("settings.routesXrayMaxRTT")} min={1} max={60000} step={100} placeholder="1000" value={x.max_rtt} onChange={(max_rtt) => setX({ max_rtt })} />
              <NumberField id="b-xray-sampling" label={t("settings.routesXraySampling")} min={1} max={100} placeholder="10" value={x.sampling} onChange={(sampling) => setX({ sampling })} />
              <NumberField
                id="b-xray-tolerance"
                label={t("settings.routesXrayTolerance")}
                min={0}
                max={100}
                placeholder="0"
                value={x.tolerance ? Math.round(x.tolerance * 100) : undefined}
                onChange={(n) => setX({ tolerance: n === undefined ? undefined : Math.min(n, 100) / 100 })}
              />
            </div>
          ) : null}
        </section>
      </div>
      {error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}
    </Card>
  );
}
