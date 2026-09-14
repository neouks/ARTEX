import type { EvidenceBodyPreview, FindingTraffic, TrafficEvidenceRole } from "../types";
import { traffic, trafficDetail } from "./data";

export class MockFindingTraffic {
  private records = new Map<string, FindingTraffic>();
  private nextID = 1;
  handle(
    id: string,
    action: string | undefined,
    bodyPart: string | undefined,
    method: string,
    body: Record<string, unknown>,
    query: URLSearchParams,
    readOnly: boolean,
  ): unknown {
    const current = this.records.get(id) ?? { finding_id: id, version: 0, report_version: 0, bindings: [] };
    if (method === "GET") {
      if (!action) return structuredClone(current);
      const binding = current.bindings.find((item) => item.id === action);
      if (!binding) throw new Error("流量证据不存在");
      if (bodyPart === "body") return this.preview(query.get("side") ?? "", Number(query.get("offset") ?? 0));
      return {
        binding: structuredClone(binding),
        request: this.preview("request", 0),
        response: this.preview("response", 0),
      };
    }
    if (readOnly) throw new Error("来源任务证据只读");
    const next = structuredClone(current);
    if (method !== "POST" && body.version !== next.version) throw new Error("证据版本已更新，请刷新后重试");
    const role = (raw: unknown): TrafficEvidenceRole => {
      if (!["baseline", "proof", "verification", "supporting"].includes(String(raw)))
        throw new Error("invalid evidence role");
      return raw as TrafficEvidenceRole;
    };
    if (method === "POST" && !action) {
      if (!Array.isArray(body.traffic_refs) || !body.traffic_refs.length) throw new Error("traffic_refs is required");
      for (const ref of body.traffic_refs) {
        const row = traffic.exchanges?.find((item) => item.id === ref.traffic_id);
        if (!row) throw new Error("流量不存在");
        const use = role(ref.role ?? "supporting");
        if (next.bindings.some((item) => item.snapshot.source_traffic_id === row.id)) continue;
        const bindingID = String(this.nextID++);
        next.bindings.push({
          id: bindingID,
          finding_id: id,
          snapshot_id: bindingID,
          role: use,
          note: String(ref.note ?? ""),
          position: next.bindings.length,
          created_at: new Date().toISOString(),
          snapshot: {
            id: bindingID,
            source_traffic_id: row.id,
            captured_at: Date.parse(row.ts) / 1000,
            url: row.url,
            method: row.method,
            status: row.status,
            content_type: row.content_type,
            req_head: `${row.method} ${new URL(row.url).pathname} HTTP/1.1\nHost: ${row.host}`,
            resp_head: `HTTP/1.1 ${row.status}\nContent-Type: ${row.content_type}`,
            req_hash: "mock-request",
            resp_hash: "mock-response",
            req_len: this.preview("request", 0).total,
            resp_len: this.preview("response", 0).total,
          },
        });
      }
    } else if (method === "PUT" && action === "order") {
      const ids = body.binding_ids;
      if (
        !Array.isArray(ids) ||
        ids.length !== next.bindings.length ||
        new Set(ids).size !== ids.length ||
        ids.some((id) => !next.bindings.some((b) => b.id === id))
      )
        throw new Error("invalid binding order");
      next.bindings.sort((a, b) => ids.indexOf(a.id) - ids.indexOf(b.id));
    } else {
      const index = next.bindings.findIndex((item) => item.id === action);
      if (index < 0) throw new Error("流量证据不存在");
      if (method === "DELETE") next.bindings.splice(index, 1);
      else if (method === "PATCH") {
        next.bindings[index].role = role(body.role);
        next.bindings[index].note = String(body.note ?? "");
      } else throw new Error("unsupported evidence operation");
    }
    next.bindings.forEach((item, index) => {
      item.position = index;
    });
    next.version++;
    this.records.set(id, next);
    return structuredClone(next);
  }
  private preview(side: string, offset: number): EvidenceBodyPreview {
    if (!["request", "response"].includes(side) || !Number.isInteger(offset) || offset < 0)
      throw new Error("invalid body range");
    const bytes = new TextEncoder().encode(
      side === "request" ? "" : trafficDetail.resp.split("\n\n").slice(1).join("\n\n"),
    );
    const end = Math.min(bytes.length, offset + 32768);
    return {
      content: new TextDecoder().decode(bytes.slice(offset, end)),
      offset,
      total: bytes.length,
      next_offset: end,
      truncated: end < bytes.length,
      binary: false,
    };
  }
}
