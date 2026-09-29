import type { Edge, TaskNode } from "./types";

// collapseDigestGraph contracts each active digest's covered members into the
// digest super-node: the members are dropped from the rendered set and every
// edge touching a member is rewired onto its digest (member→member internal
// edges collapse to a self-loop and are dropped; the resulting edges are
// deduped by src+rel+dst). covers edges are structural and never drawn.
// Superseded digests (no covers edges) are dropped entirely. A graph with no
// active digest passes through unchanged. The full node list is kept by the
// caller so the detail drawer can still resolve a folded member by id.
export function collapseDigestGraph(nodes: TaskNode[], edges: Edge[]): { viewNodes: TaskNode[]; viewEdges: Edge[] } {
  const activeDigests = new Set(nodes.filter((n) => n.type === "digest" && n.state === "active").map((n) => n.id));
  const memberToDigest = new Map<string, string>();
  for (const e of edges) {
    if (e.rel === "covers" && activeDigests.has(e.src)) memberToDigest.set(e.dst, e.src);
  }
  const viewNodes = nodes.filter((n) => (n.type === "digest" ? n.state === "active" : !memberToDigest.has(n.id)));
  const remap = (id: string) => memberToDigest.get(id) ?? id;
  const seen = new Set<string>();
  const rewired: Edge[] = [];
  const produced = new Set<string>();
  for (const e of edges) {
    if (e.rel === "covers") continue;
    const src = remap(e.src);
    const dst = remap(e.dst);
    if (src === dst) continue;
    const key = `${src}\u0000${e.rel}\u0000${dst}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (e.rel === "yields") produced.add(JSON.stringify([src, dst]));
    rewired.push(src === e.src && dst === e.dst ? e : { ...e, src, dst });
  }
  // After folding, yields and the inverse derived_from can describe the same
  // relationship. Keep the production direction regardless of input order.
  const viewEdges = rewired.filter(
    (e) => !(e.rel === "derived_from" && produced.has(JSON.stringify([e.dst, e.src]))),
  );
  return { viewNodes, viewEdges };
}

