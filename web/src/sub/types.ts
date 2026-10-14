// What the subscription page's endpoints send. They are not in openapi.json (they are
// public routes of the subscription server, not the admin API), so the shapes are written
// here by hand: keep them in step with internal/panel/subs/handler.go (/<sub>/info, the
// Mini App's /tg/session) and sub/shop.tsx (/tg/shop, /tg/pay).

export type Info = {
  name: string;
  brand: string;
  support_url?: string;
  /** Happ's crypt link: the Happ button opens it, hiding the subscription address. */
  happ_link?: string;
  state: "active" | "expiring" | "limited" | "expired" | "disabled";
  used_up: number;
  used_down: number;
  limit?: number;
  extra?: number;
  expires_at?: string;
  resets_at?: string;
  device_limit: number;
  /** How the limit counts: "online" is devices online at once, not bound ones. */
  device_mode?: "bound" | "online";
  /** The Mini App may offer a new link (the bot and the Mini App only). */
  reissue?: boolean;
  /** Only the admin reissues links. */
  reissue_off?: boolean;
  /** The admin's rules: so many new links per reissue_days (0: no limit). */
  reissue_limit?: number;
  reissue_days?: number;
  /** The subscriber may reissue the link again from then. */
  reissue_after?: string;
  speed_limit?: number;
  protocols: string[];
  binding: boolean;
  devices?: Device[];
  unbind_after?: string;
  /** The admin's unbind rules: so many per unbind_days (0: no limit), so many left now (-1: no limit). */
  /** Only the admin unbinds devices. */
  unbind_off?: boolean;
  unbind_limit?: number;
  unbind_days?: number;
  unbinds_left?: number;
  /** An unbound device may not connect again for so many hours; 0: at once, -1: until the admin lets it. */
  return_hours?: number;
  telegram?: string;
  pools?: { name: string; limit?: number; used: number; extra?: number }[];
  /** The servers of the user's connections, by their names. */
  locations?: string[];
  /** The announcement the apps show, with the user's values in it. */
  announce?: string;
  announce_url?: string;
};

/** `name` is the device's own name (the subscriber's or the admin's); "" shows what the app reported. */
export type Device = { id: number; name: string; os: string; os_version: string; model: string; app: string; shared: boolean; created_at: string; last_seen: string; used?: number };

/** One of the subscriptions a Telegram account owns (the Mini App's /tg/session). */
export type TgSub = { token: string; name: string };
