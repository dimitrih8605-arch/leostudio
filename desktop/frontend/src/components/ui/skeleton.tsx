import { cn } from "@/lib/utils";

// Skeleton uses a shimmer gradient instead of a flat pulse so the loading
// state feels alive even on slow connections. Tailwind keyframes are defined
// in globals.css.
export function Skeleton({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "relative overflow-hidden rounded-md bg-muted/50",
        "before:absolute before:inset-0 before:-translate-x-full",
        "before:animate-[shimmer_1.6s_infinite]",
        "before:bg-gradient-to-r before:from-transparent before:via-foreground/10 before:to-transparent",
        className
      )}
      {...props}
    />
  );
}
