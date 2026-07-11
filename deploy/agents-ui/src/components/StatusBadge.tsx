import { Badge } from "./ui";

const MAP: Record<string, "success" | "warning" | "destructive" | "default" | "primary"> = {
  received: "primary",
  done: "success",
  running: "warning",
  queued: "warning",
  failed: "destructive",
  ignored: "default",
};

/* Maps a router event status to a colored badge. */
export function StatusBadge({ status }: { status: string }) {
  return <Badge variant={MAP[status] ?? "default"}>{status}</Badge>;
}
