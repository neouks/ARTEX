"use client";

import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { AssetApprovalTemplate } from "@/lib/types";

export const approvalTemplateLabels: Record<AssetApprovalTemplate, string> = {
  all_assets: "全部自动批准",
  related_assets: "关联资产自动批准",
  explicit_targets: "仅用户提供资产自动批准",
};
const descriptions: Record<AssetApprovalTemplate, string> = {
  all_assets: "Agent 自主发现的合法域名和 IP 全部自动批准，可直接测试；用户封禁、撤回和删除主机仍然生效。",
  related_assets:
    "用户目标所属根域下的子域名及有解析依据的 IP 自动批准，其他发现等待人工审批。获准主机的所有端口、服务和接口均可测试。",
  explicit_targets:
    "仅任务描述明确提供、创建时关联及任务内手动添加的域名/IP自动批准，其他发现等待人工审批。审批不限制端口、服务或接口。",
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
