import type { Finding, FindingStatus, Severity } from "@/lib/types";

export const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];
export const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];
export const UNASSIGNED_TASK = "__unassigned__";
export function findingRowKey(finding: Finding): string {
  return finding.finding_id
    ? `finding:${finding.finding_id}`
    : `node:${finding.task_id ?? UNASSIGNED_TASK}:${finding.id}`;
}
export function isSameFinding(left: Finding, right: Finding) {
  return findingRowKey(left) === findingRowKey(right);
}
export function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN");
}
export function selectFinding(items: Finding[], key: string | null) {
  return items.find((item) => findingRowKey(item) === key) ?? items[0] ?? null;
}
