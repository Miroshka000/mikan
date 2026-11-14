import { describe, expect, it } from "vitest";
import { t } from "../../../i18n";
import { eventLabel, failureText, groupedTypes, shortAddress, stateOf, type Delivery, type Webhook } from "./common";

// The fields the helpers read; the rest of a view does not matter to them.
const webhook = (p: Partial<Webhook>) => ({ enabled: true, ...p }) as Webhook;
const delivery = (status: Delivery["status"]) => ({ status }) as Delivery;

describe("shortAddress", () => {
  it("keeps an address whole while nothing in it may be a token", () => {
    expect(shortAddress("https://hooks.example.com/mikan")).toBe("https://hooks.example.com/mikan");
    expect(shortAddress("http://10.0.0.5:8080/in")).toBe("http://10.0.0.5:8080/in");
    expect(shortAddress("https://example.com/")).toBe("https://example.com");
  });
  it("cuts what follows the first part of the path, where a Discord or Slack token sits", () => {
    expect(shortAddress("https://discord.com/api/webhooks/123456/AbCdEfGh")).toBe("https://discord.com/api/…");
    expect(shortAddress("https://hooks.slack.com/services/T000/B000/XXXX")).toBe("https://hooks.slack.com/services/…");
  });
  it("cuts a query, which may carry a token as well", () => {
    expect(shortAddress("https://example.com/hook?token=abc")).toBe("https://example.com/hook/…");
    expect(shortAddress("https://example.com/?token=abc")).toBe("https://example.com/…");
  });
  it("shows what is not an address as it is", () => {
    expect(shortAddress("not an address")).toBe("not an address");
  });
});

describe("groupedTypes", () => {
  it("sorts the types into sections by what they are about, in the order of the sections", () => {
    const got = groupedTypes(["node.down", "payment.paid", "user.created", "balance.topup", "device.bound", "trial.started", "traffic.90"]);
    expect(got.map((g) => g.group)).toEqual(["payments", "subscribers", "devices", "nodes"]);
    expect(got[0]!.types).toEqual(["payment.paid", "balance.topup"]);
    expect(got[1]!.types).toEqual(["user.created", "trial.started", "traffic.90"]);
  });
  it("puts a type this version does not know in its own section, last, and leaves empty sections out", () => {
    const got = groupedTypes(["subscription.renewed", "node.up"]);
    expect(got).toEqual([
      { group: "nodes", types: ["node.up"] },
      { group: "other", types: ["subscription.renewed"] },
    ]);
    expect(groupedTypes([])).toEqual([]);
  });
});

describe("stateOf", () => {
  it("is paused when the address is off, whatever its last delivery was", () => {
    expect(stateOf(webhook({ enabled: false, last: delivery("retrying") }))).toBe("paused");
  });
  it("has had no events yet while there is no delivery", () => {
    expect(stateOf(webhook({}))).toBe("idle");
  });
  it("is failing while the last event is being retried, failed once it was given up, and ok after a delivery", () => {
    expect(stateOf(webhook({ last: delivery("retrying") }))).toBe("failing");
    expect(stateOf(webhook({ last: delivery("failed") }))).toBe("failed");
    expect(stateOf(webhook({ last: delivery("delivered") }))).toBe("ok");
  });
});

describe("failureText", () => {
  it("says a known reason in words and adds the status the receiver answered", () => {
    expect(failureText("status", 502)).toBe(`${t("webhooks.failure.status")}, ${t("webhooks.http", { code: 502 })}`);
    expect(failureText("timeout", 0)).toBe(t("webhooks.failure.timeout"));
  });
  it("shows a reason this version has no words for as its code, and a bare status as the status", () => {
    expect(failureText("quota", 0)).toBe("quota");
    expect(failureText("", 404)).toBe(t("webhooks.http", { code: 404 }));
  });
});

describe("eventLabel", () => {
  it("names an event the panel sends in words and an unknown one by its type", () => {
    expect(eventLabel("payment.paid")).toBe(t("webhooks.events.payment.paid"));
    expect(eventLabel("traffic.90")).toBe(t("webhooks.events.traffic.90"));
    expect(eventLabel("subscription.renewed")).toBe("subscription.renewed");
  });
});
