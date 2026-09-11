"use client";

import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { AssetApprovalTemplate } from "@/lib/types";

export const approvalTemplateLabels: Record<AssetApprovalTemplate, string> = {
  all_assets: "全部资产免审批",
  related_assets: "关联资产自动批准",
  explicit_targets: "明确目标免审批",
};
const descriptions: Record<AssetApprovalTemplate, string> = {
  all_assets: "包括 Agent 自主发现的合法资产，全部自动批准；手动封禁、撤回和删除仍然生效。",
  related_assets: "用户目标及关联根域、子域和有解析依据的 IP 自动批准，其他发现等待审批。",
  explicit_targets: "仅明确提供的目标及服务接口免审批；明确网段、通配范围按指定范围授权。",
};
export function AssetApprovalTemplateField({
  value,
  onChange,
  disabled = false,
}: {
  value: AssetApprovalTemplate;
  onChange: (value: AssetApprovalTemplate) => void;
  disabled?: boolean;
}) {
  return (
    <FieldGroup>
      <Field data-disabled={disabled}>
        <FieldLabel id="asset-approval-template-label">资产审批模板</FieldLabel>
        <ToggleGroup
          type="single"
          variant="outline"
          value={value}
          disabled={disabled}
          aria-labelledby="asset-approval-template-label"
          className="flex-wrap"
          onValueChange={(next) => {
            if (next in approvalTemplateLabels) onChange(next as AssetApprovalTemplate);
          }}
        >
          {Object.entries(approvalTemplateLabels).map(([key, label]) => (
            <ToggleGroupItem key={key} value={key}>
              {label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <FieldDescription>{descriptions[value]}</FieldDescription>
      </Field>
    </FieldGroup>
  );
}
