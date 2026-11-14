import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { bytes, days, daysUntil, expiryText, period, periodLabel, waitingDays } from "./format";

describe("bytes", () => {
  it("names the unit the number is in", () => {
    expect(bytes(0)).toBe(`0 ${t("units.b")}`);
    expect(bytes(512)).toBe(`512 ${t("units.b")}`);
    expect(bytes(1536 * 1024)).toMatch(/^1[,.]5 /);
  });
  it("goes to the next unit when rounding reaches it", () => {
    // 1048575 bytes round to 1024 KB: that is 1 MB, not "1024 KB".
    expect(bytes(1048575)).toBe(bytes(1048576));
  });
});

describe("period", () => {
  it("says the usual periods in words and the others in days", () => {
    expect(period(1)).toBe(t("sub.periodDay"));
    expect(period(7)).toBe(t("sub.periodWeek"));
    expect(period(30)).toBe(t("sub.periodMonth"));
    expect(period(5)).toBe(t("sub.periodDays", { n: 5 }));
  });
  it("is a label with the first letter up", () => {
    const p = period(7);
    expect(periodLabel(7)).toBe(p.charAt(0).toUpperCase() + p.slice(1));
  });
});

describe("plurals", () => {
  it("take the Russian forms by the number", () => {
    expect(days(1)).toMatch(/^1 /);
    expect(days(2)).not.toBe(days(5).replace("5", "2"));
    expect(days(21)).toBe(days(1).replace("1", "21"));
  });
});

describe("daysUntil and expiryText", () => {
  const now = new Date(2026, 9, 21, 12, 0).getTime();
  it("count calendar days, not periods of 24 hours", () => {
    // Tomorrow morning is one day away, though it is fewer than 24 hours.
    expect(daysUntil(new Date(2026, 9, 22, 9, 0).toISOString(), now)).toBe(1);
    expect(daysUntil(new Date(2026, 9, 21, 23, 0).toISOString(), now)).toBe(0);
  });
  it("calls a past moment expired, even earlier today", () => {
    expect(expiryText(new Date(2026, 9, 21, 11, 0).toISOString(), now).tone).toBe("bad");
    expect(expiryText(new Date(2026, 9, 21, 13, 0).toISOString(), now).tone).not.toBe("bad");
  });
});

describe("waitingDays", () => {
  it("keeps the whole term whatever the time of day, as the clock moves on every ten minutes", () => {
    // The term was moved on at 23:58; at 00:05 the calendar says 29 days to its end, the term has 30.
    const moved = new Date(2026, 9, 21, 23, 58).getTime();
    const end = new Date(moved + 30 * 86_400_000).toISOString();
    const after = new Date(2026, 9, 22, 0, 5).getTime();
    expect(daysUntil(end, after)).toBe(29);
    expect(waitingDays(end, after)).toBe(30);
    expect(waitingDays(end, moved)).toBe(30);
  });
  it("is zero for a term that is over", () => {
    expect(waitingDays(new Date(2026, 9, 21, 11, 0).toISOString(), new Date(2026, 9, 21, 12, 0).getTime())).toBe(0);
  });
});
