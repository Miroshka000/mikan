// When a tariff's traffic is given anew. A new period zeroes the main traffic and the server
// groups with a limit of their own together (domain.StartPeriod, the scheduled reset), so the
// reset matters while either has a limit: the groups' alone with the main traffic unlimited.
import type { Tariff } from "../../../api/client";
import { quotasOf, type TariffQuotas } from "../sale-rules";

type Reset = Tariff["reset_strategy"];

/** What of a tariff the reset reads: whether the main traffic has a limit, and each group's mode. */
type Limits = Pick<TariffQuotas, "trafficLimited" | "groups">;

/** Whether a new period has traffic of the tariff to give anew: the main traffic with a limit,
 * or a server group with a limit of its own. */
export function renewsTraffic(q: Limits): boolean {
  return q.trafficLimited || Object.values(q.groups).some((g) => g.mode === "own");
}

/** The reset a tariff is saved with: the one chosen while a period renews its traffic, none
 * when nothing of it has a limit (the form does not ask then). */
export function resetToSave(q: Limits, chosen: Reset): Reset {
  return renewsTraffic(q) ? chosen : "none";
}

/** The reset the form starts from: the tariff's own while a period renews its traffic. A new
 * tariff, and one saved with nothing to renew (its none was nobody's choice), start from every
 * 30 days, which the form shows once something gets a limit. */
export function resetOf(tr: Tariff | null): Reset {
  return tr && renewsTraffic(quotasOf(tr)) ? tr.reset_strategy : "period";
}
