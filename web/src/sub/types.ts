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
  speed_limit?: number;
  protocols: string[];
  binding: boolean;
  devices?: Device[];
  unbind_after?: string;
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
