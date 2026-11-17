// When traffic is given anew, as the server does it (domain.StartPeriod, the scheduled reset):
// a new period zeroes the main traffic and the server groups with a limit of their own
// together, so the reset matters while either has a limit, the groups' alone with the main
// traffic unlimited. The one copy the tariff's form and the client's card ask.
import type { Schemas, Tariff } from "../../api/client";

type Mode = Schemas["PoolSetting"]["mode"];

/** When traffic is reset: every period, monthly on the billing day, or never. */
export type Reset = Tariff["reset_strategy"];

/** What the reset reads of a tariff or a client: whether the main traffic has a limit, and
 * each group's mode. */
export type Limits = { trafficLimited: boolean; groups: Record<number, { mode: Mode }> };

/** Whether a new period has traffic to give anew: the main traffic with a limit, or a server
 * group with a limit of its own. */
export function renewsTraffic(q: Limits): boolean {
  return q.trafficLimited || Object.values(q.groups).some((g) => g.mode === "own");
}

/** The reset a tariff is saved with: the one chosen while a period renews its traffic, none
 * when nothing of it has a limit (the form does not ask then). */
export function resetToSave(q: Limits, chosen: Reset): Reset {
  return renewsTraffic(q) ? chosen : "none";
}

/** The reset the client's card sends with the groups: the one chosen while a period renews
 * something of the client's and it differs from the client's own. Otherwise nothing is
 * sent, and the client's stays as it is (a hidden choice changes nothing). */
export function resetToSend(q: Limits, chosen: Reset, saved: Reset): Reset | undefined {
  return renewsTraffic(q) && chosen !== saved ? chosen : undefined;
}

/** The reset a form starts from: the one saved, unless it is none on something with nothing
 * to renew. Nobody chose that none (the tariff's form saves it so), and the form offers every
 * 30 days, a new tariff's reset, once something gets a limit. */
export function startReset(q: Limits, saved: Reset): Reset {
  return saved === "none" && !renewsTraffic(q) ? "period" : saved;
}
