import { usePaymentSettings } from "../api/hooks";
import { NAV } from "./nav";

export type NavItem = (typeof NAV)[number];

/**
 * The sections of the menu as this panel shows them. Payments are listed while subscriptions
 * are sold: with selling off the section has nothing to do in the menu, and is turned on in
 * Settings → General. Until the panel has answered, the section stays, so a seller's menu
 * does not blink.
 */
export function useNav(): readonly NavItem[] {
  const selling = usePaymentSettings().data?.enabled;
  return selling === false ? NAV.filter((n) => n.key !== "payments") : NAV;
}
