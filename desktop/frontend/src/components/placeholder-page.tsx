import type { LucideIcon } from "lucide-react";

// Reusable empty-state surface shown for pages that haven't been wired yet.
// Keeps the visual language consistent while we port features one by one.
export function PlaceholderPage({
  icon: Icon,
  title,
  description,
  hint,
}: {
  icon: LucideIcon;
  title: string;
  description: string;
  hint?: string;
}) {
  return (
    <div className="flex h-full items-center justify-center p-10">
      <div className="flex max-w-md flex-col items-center gap-4 text-center">
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-accent text-accent-foreground">
          <Icon className="h-7 w-7" strokeWidth={2.2} />
        </div>
        <div className="space-y-1">
          <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
        {hint ? (
          <p className="rounded-md border border-dashed border-border bg-card/60 px-3 py-2 text-xs text-muted-foreground">
            {hint}
          </p>
        ) : null}
      </div>
    </div>
  );
}
