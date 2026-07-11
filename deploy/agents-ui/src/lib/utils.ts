import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// parseArgv splits a command line into argv with shell-like quoting, so an agent command such as
// `claude -p "review this PR"` becomes ["claude","-p","review this PR"] instead of being naively
// split on whitespace (which would break the quoted argument). Supports single and double quotes
// and backslash escapes inside double quotes.
export function parseArgv(input: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote: '"' | "'" | null = null;
  let token = false; // whether the current token has started (so "" stays a real empty arg)
  for (let i = 0; i < input.length; i++) {
    const ch = input[i];
    if (quote) {
      if (ch === quote) quote = null;
      else if (ch === "\\" && quote === '"' && i + 1 < input.length) cur += input[++i];
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      token = true;
    } else if (/\s/.test(ch)) {
      if (token) {
        out.push(cur);
        cur = "";
        token = false;
      }
    } else {
      cur += ch;
      token = true;
    }
  }
  if (token) out.push(cur);
  return out;
}
