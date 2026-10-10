//! The time as text, from the system clock: no `date` to run, no "now" to fall back on
//! that two backups of one minute would share.

use std::time::{SystemTime, UNIX_EPOCH};

/// A moment in UTC.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Utc {
    pub year: i64,
    pub month: u32,
    pub day: u32,
    pub hour: u32,
    pub minute: u32,
    pub second: u32,
}

impl Utc {
    pub fn now() -> Self {
        let secs = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
        Self::from_unix(secs as i64)
    }

    pub fn from_unix(secs: i64) -> Self {
        let days = secs.div_euclid(86_400);
        let rest = secs.rem_euclid(86_400) as u32;
        // Days since 1970-01-01 to a civil date (Howard Hinnant's algorithm).
        let z = days + 719_468;
        let era = z.div_euclid(146_097);
        let doe = z.rem_euclid(146_097);
        let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
        let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
        let mp = (5 * doy + 2) / 153;
        let day = (doy - (153 * mp + 2) / 5 + 1) as u32;
        let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
        let year = yoe + era * 400 + i64::from(month <= 2);
        Self { year, month, day, hour: rest / 3600, minute: rest % 3600 / 60, second: rest % 60 }
    }

    /// 20261001-181929, for file names.
    pub fn stamp(&self) -> String {
        format!("{:04}{:02}{:02}-{:02}{:02}{:02}", self.year, self.month, self.day, self.hour, self.minute, self.second)
    }

    /// 2026-10-01T18:19:29Z, as the panel reads it.
    pub fn rfc3339(&self) -> String {
        format!("{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z", self.year, self.month, self.day, self.hour, self.minute, self.second)
    }
}

pub fn rfc3339() -> String {
    Utc::now().rfc3339()
}

/// Seconds since 1970-01-01 of a civil date (the inverse of Utc::from_unix).
fn unix_of(year: i64, month: u32, day: u32, hour: u32, minute: u32, second: u32) -> i64 {
    let y = year - i64::from(month <= 2);
    let era = y.div_euclid(400);
    let yoe = y.rem_euclid(400);
    let mp = i64::from((month + 9) % 12);
    let doy = (153 * mp + 2) / 5 + i64::from(day) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    days * 86_400 + i64::from(hour) * 3600 + i64::from(minute) * 60 + i64::from(second)
}

fn number(s: &str, len: usize) -> Option<u32> {
    if s.len() == len && s.bytes().all(|b| b.is_ascii_digit()) { s.parse().ok() } else { None }
}

/// A date and time as the Go side writes them, 2026-10-10T12:00:00.123456789+03:00 or ...Z,
/// as seconds since 1970; None for anything else.
pub fn parse_rfc3339(s: &str) -> Option<i64> {
    let s = s.trim();
    let (date, rest) = s.split_once(['T', 't'])?;
    let mut d = date.split('-');
    let (year, month, day) = (number(d.next()?, 4)?, number(d.next()?, 2)?, number(d.next()?, 2)?);
    if d.next().is_some() {
        return None;
    }
    let zone = rest.find(['Z', 'z', '+', '-']).filter(|&i| i > 0)?;
    let (time, zone) = rest.split_at(zone);
    let time = time.split_once('.').map_or(time, |(t, frac)| if frac.bytes().all(|b| b.is_ascii_digit()) { t } else { "" });
    let mut t = time.split(':');
    let (hour, minute, second) = (number(t.next()?, 2)?, number(t.next()?, 2)?, number(t.next()?, 2)?);
    if t.next().is_some() || !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || minute > 59 || second > 60 {
        return None;
    }
    let offset = match zone {
        "Z" | "z" => 0,
        z => {
            let sign = if z.starts_with('-') { -1 } else { 1 };
            let (h, m) = z[1..].split_once(':')?;
            sign * (i64::from(number(h, 2)?) * 3600 + i64::from(number(m, 2)?) * 60)
        }
    };
    Some(unix_of(i64::from(year), month, day, hour, minute, second) - offset)
}

/// An HTTP Date header (RFC 7231), Sun, 06 Nov 1994 08:49:37 GMT, as seconds since 1970.
pub fn parse_http_date(s: &str) -> Option<i64> {
    const MONTHS: [&str; 12] = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    let mut w = s.split_whitespace();
    let name = w.next()?;
    if !name.ends_with(',') {
        return None;
    }
    let day = number(w.next()?, 2)?;
    let name = w.next()?;
    let month = MONTHS.iter().position(|m| *m == name)? as u32 + 1;
    let year = number(w.next()?, 4)?;
    let mut t = w.next()?.split(':');
    let (hour, minute, second) = (number(t.next()?, 2)?, number(t.next()?, 2)?, number(t.next()?, 2)?);
    if t.next().is_some() || w.next() != Some("GMT") || w.next().is_some() {
        return None;
    }
    if !(1..=31).contains(&day) || hour > 23 || minute > 59 || second > 60 {
        return None;
    }
    Some(unix_of(i64::from(year), month, day, hour, minute, second))
}

/// Seconds since 1970 of a system time.
pub fn unix(t: SystemTime) -> i64 {
    match t.duration_since(UNIX_EPOCH) {
        Ok(d) => d.as_secs() as i64,
        Err(e) => -(e.duration().as_secs() as i64),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn civil_dates() {
        assert_eq!(Utc::from_unix(0).rfc3339(), "1970-01-01T00:00:00Z");
        assert_eq!(Utc::from_unix(951_782_400).rfc3339(), "2000-02-29T00:00:00Z");
        assert_eq!(Utc::from_unix(1_790_876_369).rfc3339(), "2026-10-01T17:39:29Z");
        assert_eq!(Utc::from_unix(1_790_876_369).stamp(), "20261001-173929");
        assert_eq!(Utc::from_unix(4_107_542_399).rfc3339(), "2100-02-28T23:59:59Z");
        assert_eq!(Utc::from_unix(-1).rfc3339(), "1969-12-31T23:59:59Z");
        assert!(Utc::now().year >= 2026);
    }

    #[test]
    fn dates_from_text() {
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_rfc3339("2026-10-01T17:39:29Z"), Some(1_790_876_369));
        assert_eq!(parse_rfc3339("2026-10-01T17:39:29.123456789Z"), Some(1_790_876_369));
        assert_eq!(parse_rfc3339("2026-10-01T20:39:29+03:00"), Some(1_790_876_369));
        assert_eq!(parse_rfc3339("2026-10-01T12:09:29-05:30"), Some(1_790_876_369));
        assert_eq!(parse_rfc3339("2000-02-29T00:00:00Z"), Some(951_782_400));
        for bad in
            ["", "2026-10-01", "2026-10-01T17:39:29", "2026-13-01T00:00:00Z", "2026-10-01T25:00:00Z", "yesterday", "2026-10-01T17:39:29.xZ"]
        {
            assert_eq!(parse_rfc3339(bad), None, "{bad:?}");
        }
        // round trip with the formatter
        for secs in [0, 86_399, 951_782_400, 1_790_876_369, 4_107_542_399] {
            assert_eq!(parse_rfc3339(&Utc::from_unix(secs).rfc3339()), Some(secs));
        }
        assert_eq!(parse_http_date("Sun, 06 Nov 1994 08:49:37 GMT"), Some(784_111_777));
        assert_eq!(parse_http_date("Thu, 01 Oct 2026 17:39:29 GMT"), Some(1_790_876_369));
        for bad in [
            "",
            "Sun, 06 Nov 1994 08:49:37 UTC",
            "06 Nov 1994 08:49:37 GMT",
            "Sun, 06 Foo 1994 08:49:37 GMT",
            "Sunday, 06-Nov-94 08:49:37 GMT",
        ] {
            assert_eq!(parse_http_date(bad), None, "{bad:?}");
        }
        assert_eq!(unix(UNIX_EPOCH + std::time::Duration::from_secs(5)), 5);
    }
}
