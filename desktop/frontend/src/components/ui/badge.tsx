import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-medium ring-1 ring-inset",
  {
    variants: {
      tone: {
        // Tone names map to backend status strings for clarity.
        neutral: "bg-muted text-muted-foreground ring-border",
        success: "bg-emerald-500/15 text-emerald-300 ring-emerald-500/30",
        warning: "bg-amber-500/15 text-amber-300 ring-amber-500/30",
        danger: "bg-red-500/15 text-red-300 ring-red-500/30",
        info: "bg-primary/15 text-primary ring-primary/30",
      },
    },
    defaultVariants: { tone: "neutral" },
  }
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, tone, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ tone }), className)} {...props} />
  );
}
