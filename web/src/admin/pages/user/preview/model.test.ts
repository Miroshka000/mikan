import { describe, expect, it } from "vitest";
import { bodyFile, leftByReason, madeByTemplate, recentAgents, serversFrom, type PreviewLeft } from "./model";

const left = (inbound_id: number, reason: PreviewLeft["reason"]): PreviewLeft => ({ inbound_id, inbound_name: `in${inbound_id}`, node_id: 1, reason });

describe("leftByReason", () => {
  it("groups by reason, why the client lacks a server first, and drops the reasons there are none of", () => {
    const got = leftByReason([left(1, "app"), left(2, "not_in_group"), left(3, "app"), left(4, "balance")]);
    expect(got.map(([reason, list]) => [reason, list.map((l) => l.inbound_id)])).toEqual([
      ["not_in_group", [2]],
      ["balance", [4]],
      ["app", [1, 3]],
    ]);
  });
  it("is empty for nothing left out", () => {
    expect(leftByReason([])).toEqual([]);
  });
});

describe("recentAgents", () => {
  it("keeps the distinct apps, newest first, at most n, without empty ones", () => {
    const items = [{ agent: "Happ/3.6.0" }, { agent: " " }, { agent: "Happ/3.6.0 " }, { agent: "sing-box 1.12.0" }, { agent: "v2rayNG/1.9" }];
    expect(recentAgents(items, 2)).toEqual(["Happ/3.6.0", "sing-box 1.12.0"]);
    expect(recentAgents([], 4)).toEqual([]);
  });
});

describe("bodyFile", () => {
  it("names the file by the format", () => {
    expect(bodyFile("clash")).toEqual({ ext: "yaml", type: "text/yaml" });
    expect(bodyFile("singbox").ext).toBe("json");
    expect(bodyFile("xray").ext).toBe("json");
    expect(bodyFile("uri")).toEqual({ ext: "txt", type: "text/plain" });
    expect(bodyFile("").ext).toBe("txt");
  });
});

describe("serversFrom", () => {
  it("puts the card's own list over the group, and the group over the tariff", () => {
    expect(serversFrom({ own: true, by_group: true }, 3)).toBe("own");
    expect(serversFrom({ own: false, by_group: true }, 3)).toBe("group");
    expect(serversFrom({ own: false, by_group: false }, 3)).toBe("tariff");
    expect(serversFrom({ own: false, by_group: false }, null)).toBe("all");
  });
});

describe("madeByTemplate", () => {
  it("is a served profile of Clash, sing-box or Xray only", () => {
    expect(madeByTemplate({ outcome: "served", format: "clash" })).toBe(true);
    expect(madeByTemplate({ outcome: "served", format: "xray" })).toBe(true);
    expect(madeByTemplate({ outcome: "served", format: "uri" })).toBe(false);
    expect(madeByTemplate({ outcome: "served", format: "html" })).toBe(false);
    expect(madeByTemplate({ outcome: "stub", format: "clash" })).toBe(false);
  });
});
