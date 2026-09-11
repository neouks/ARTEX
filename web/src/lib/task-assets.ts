import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "应用",
  endpoint: "接口",
  ip: "IP",
  root_domain: "根域名",
  service: "服务",
  subdomain: "子域名",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Agent 发现",
  anchor: "黑板锚点",
  api: "用户提供",
  company: "用户提供",
  direct: "用户提供",
  legacy: "历史关联",
  manual: "用户提供",
  system: "系统关联",
  task: "用户提供",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
