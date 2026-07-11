import { useState } from "react";
import { Activity, ChevronDown, ChevronRight, GitPullRequest, RefreshCw } from "lucide-react";
import type { EventRec } from "@/lib/api";
import { Button, Card, CardContent, CardHeader, CardTitle } from "./ui";
import { StatusBadge } from "./StatusBadge";

const BORDER: Record<string, string> = {
  received: "border-l-primary",
  done: "border-l-success",
  failed: "border-l-destructive",
  queued: "border-l-warning",
  running: "border-l-warning",
  ignored: "border-l-border",
};

function Row({ e }: { e: EventRec }) {
  const [open, setOpen] = useState(false);
  const hasOutput = !!e.output;
  return (
    <div className={`rounded-r-md border-l-2 bg-background/40 px-3 py-2 ${BORDER[e.status] ?? "border-l-border"}`}>
      <div className="flex items-center justify-between gap-2">
        <button
          className="flex min-w-0 items-center gap-1.5 text-left"
          onClick={() => hasOutput && setOpen(!open)}
          disabled={!hasOutput}
        >
          {hasOutput ? (
            open ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />
          ) : (
            <span className="w-3.5" />
          )}
          <span className="truncate font-semibold">{e.handle ? `@${e.handle}` : e.event}</span>
        </button>
        <StatusBadge status={e.status} />
      </div>
      <div className="ml-5 mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
        <span>{e.repo}{e.issue ? `#${e.issue}` : ""}</span>
        {e.is_pull && (
          <span className="inline-flex items-center gap-0.5">
            <GitPullRequest className="h-3 w-3" /> PR
          </span>
        )}
        <span>· {e.kind}</span>
        <span>· {e.trigger || e.event}</span>
        {e.sender && <span>· by {e.sender}</span>}
        {e.duration_s > 0 && <span>· {e.duration_s.toFixed(1)}s</span>}
        {e.pushed && <span className="text-success">· pushed</span>}
        <span>· {new Date(e.ts).toLocaleTimeString()}</span>
      </div>
      {e.detail && <div className="ml-5 mt-0.5 text-xs text-muted-foreground">{e.detail}</div>}
      {open && hasOutput && (
        <pre className="ml-5 mt-2 max-h-64 overflow-auto rounded-md border bg-popover p-2 font-mono text-xs">
          {e.output}
        </pre>
      )}
    </div>
  );
}

export function Timeline({ events, reload }: { events: EventRec[]; reload: () => void }) {
  return (
    <Card className="flex h-full flex-col">
      <CardHeader className="flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2">
          <Activity className="h-4 w-4 text-primary" /> Timeline
          <span className="text-xs font-normal text-muted-foreground">{events.length} events</span>
        </CardTitle>
        <Button size="sm" variant="ghost" onClick={reload}>
          <RefreshCw className="h-3.5 w-3.5" /> refresh
        </Button>
      </CardHeader>
      <CardContent className="flex-1 space-y-1.5 overflow-auto">
        {events.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No events yet — @mention an agent on a PR or issue, or use Manual trigger.
          </p>
        )}
        {events.map((e) => (
          <Row key={e.id} e={e} />
        ))}
      </CardContent>
    </Card>
  );
}
