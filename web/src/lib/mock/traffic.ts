import type { TrafficDetail, TrafficResp } from "../types";

// Keep mutations separate from fixtures; reloading the demo restores its data.
export class MockTraffic {
  private rows: NonNullable<TrafficResp["exchanges"]>;
  constructor(private readonly seed: TrafficResp, private readonly detail: TrafficDetail) {
    this.rows = structuredClone(seed.exchanges ?? []);
  }
  hosts() {
    const counts = new Map<string, number>();
    for (const row of this.rows) counts.set(row.host, (counts.get(row.host) ?? 0) + 1);
    return { hosts: [...counts].map(([host, count]) => ({ host, count })) };
  }
  remove(host?: string, hosts?: string[]) {
    const before = this.rows.length;
    this.rows = this.rows.filter((row) => hosts ? !hosts.includes(row.host) : host ? !row.host.includes(host) : false);
    return { deleted: before - this.rows.length, reclaimed: 0 };
  }
  page(q: URLSearchParams): TrafficResp {
    const contains = (value: string, term: string) => value.toLowerCase().includes(term.toLowerCase());
    const body = q.get("body")?.trim() ?? "";
    const status = q.get("status")?.trim().toLowerCase() ?? "";
    const raw = `${this.detail.req}\n${this.detail.resp}`;
    const rows = this.rows.filter((row) => {
      if (!contains(row.host, q.get("host") ?? "")) return false;
      const method = q.get("method");
      if (method && method !== "all" && row.method !== method.toUpperCase()) return false;
      if (!contains(new URL(row.url).pathname, q.get("path") ?? "")) return false;
      if (/^[1-5]xx$/.test(status) && Math.floor(row.status / 100) !== Number(status[0])) return false;
      if (/^\d+$/.test(status) && row.status !== Number(status)) return false;
      for (const bound of ["min", "max"] as const) {
        const value = q.get(`resp_${bound}`);
        if (value && (bound === "min" ? row.resp_len < Number(value) : row.resp_len > Number(value))) return false;
      }
      if ([...body].length >= 3 && !contains(raw, body)) return false;
      const term = q.get("q") ?? "";
      return contains(`${row.host} ${row.url} ${row.method} ${row.status} ${row.content_type}`, term) ||
        ([...term].length >= 3 && contains(raw, term));
    });
    const field = q.get("sort"), direction = q.get("order") === "asc" ? 1 : -1;
    rows.sort((a, b) => {
      const diff = field === "status" ? a.status - b.status : field === "resp_len" ? a.resp_len - b.resp_len : Date.parse(a.ts) - Date.parse(b.ts);
      return diff * direction || b.id.localeCompare(a.id);
    });
    const page = Math.max(0, Number(q.get("page")) || 0);
    const requested = Number(q.get("size"));
    const size = requested > 0 && requested <= 500 ? requested : 100;
    return { ...this.seed, count: this.rows.length, total: rows.length, page, size, exchanges: rows.slice(page * size, (page + 1) * size) };
  }
}
