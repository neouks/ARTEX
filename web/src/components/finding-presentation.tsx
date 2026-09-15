"use client";

import * as React from "react";

import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { cn } from "@/lib/utils";

export function useFindingPresentation(key: string) {
  const [layout, setLayout] = React.useState<"flat" | "split">("flat");
  const [detailOpen, setDetailOpen] = React.useState(false);
  React.useEffect(() => {
    setLayout(getLocalStorageValue(key) === "split" ? "split" : "flat");
  }, [key]);
  const changeLayout = (value: string) => {
    if (value !== "flat" && value !== "split") return;
    setLayout(value);
    setDetailOpen(false);
    setLocalStorageValue(key, value);
  };
  return { layout, changeLayout, detailOpen, setDetailOpen };
}

export function FindingLayoutToggle({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size="sm"
      spacing={0}
      className="shrink-0"
      value={value}
      onValueChange={onChange}
      aria-label="漏洞展示方式"
    >
      <ToggleGroupItem value="flat">平铺</ToggleGroupItem>
      <ToggleGroupItem value="split">分栏</ToggleGroupItem>
    </ToggleGroup>
  );
}

export function FindingPresentation({
  layout,
  detailOpen,
  onOpenChange,
  detail,
  children,
}: {
  layout: "flat" | "split";
  detailOpen: boolean;
  onOpenChange: (open: boolean) => void;
  detail: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "grid min-w-0 items-start gap-4",
        layout === "split" && "xl:grid-cols-[minmax(20rem,0.9fr)_minmax(0,2fr)]",
      )}
    >
      {children}
      {layout === "split" ? (
        <div className="min-w-0">{detail}</div>
      ) : (
        <Dialog open={detailOpen} onOpenChange={onOpenChange}>
          <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-5xl" aria-describedby={undefined}>
            <DialogHeader>
              <DialogTitle>漏洞详情</DialogTitle>
            </DialogHeader>
            <div className="min-w-0">{detailOpen && detail}</div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
