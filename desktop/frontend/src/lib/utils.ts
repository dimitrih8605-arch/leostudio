import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// Standard shadcn/ui helper. Combines clsx (conditional classes) with
// tailwind-merge (conflict resolution) so component variants stay clean.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
