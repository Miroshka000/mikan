/**
 * The widest Hysteria2 hopping range the panel takes for a new port (domain.MaxHopPorts):
 * the core runs a QUIC server of its own on every port of a range. A wider range saved
 * before keeps working.
 */
export const MAX_HOP_PORTS = 1000;

/** How many ports spec takes: 1 for a port, every port of a range ("20000-20099"), 0 for
 * neither. The grammar is the panel's (domain.ParsePort): a range ends above its start. */
export function hopPorts(spec: string): number {
  const m = /^(\d{1,5})(?:-(\d{1,5}))?$/.exec(spec.trim());
  if (!m) return 0;
  const lo = Number(m[1]);
  if (lo < 1 || lo > 65535) return 0;
  if (m[2] === undefined) return 1;
  const hi = Number(m[2]);
  if (hi <= lo || hi > 65535) return 0;
  return hi - lo + 1;
}

/** About how many MB of a node's memory a range takes: 0.12 MB a port, as a node with
 * 20,001 of them held 2.4 GB. */
export function hopMemoryMB(ports: number): number {
  return Math.round(ports * 0.12);
}
