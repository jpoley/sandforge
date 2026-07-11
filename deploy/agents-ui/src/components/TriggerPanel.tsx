import { useState } from "react";
import { Zap } from "lucide-react";
import { api, type Agent } from "@/lib/api";
import { Button, Card, CardContent, CardHeader, CardTitle, Input, Label, Switch } from "./ui";

/* Manual handoff: invoke an agent against an issue/PR without waiting for a webhook. */
export function TriggerPanel({ agents, onDone }: { agents: Agent[]; onDone: () => void }) {
  const [owner, setOwner] = useState("");
  const [repo, setRepo] = useState("");
  const [issue, setIssue] = useState("");
  const [handle, setHandle] = useState(agents[0]?.handle ?? "");
  const [isPull, setIsPull] = useState(false);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  async function go() {
    setMsg("");
    setBusy(true);
    try {
      await api.trigger({ owner, repo, issue: parseInt(issue || "0", 10), is_pull: isPull, handle, comment });
      setMsg("Triggered — see the timeline.");
      onDone();
    } catch (e) {
      setMsg("Failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Zap className="h-4 w-4 text-primary" /> Manual trigger
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Owner</Label>
            <Input value={owner} placeholder="sandforge" onChange={(e) => setOwner(e.target.value)} />
          </div>
          <div>
            <Label>Repo</Label>
            <Input value={repo} placeholder="tasks" onChange={(e) => setRepo(e.target.value)} />
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label>Issue / PR #</Label>
            <Input value={issue} type="number" onChange={(e) => setIssue(e.target.value)} />
          </div>
          <div>
            <Label>Agent</Label>
            <select
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={handle}
              onChange={(e) => setHandle(e.target.value)}
            >
              {agents.map((a) => (
                <option key={a.handle} value={a.handle}>
                  @{a.handle}
                </option>
              ))}
            </select>
          </div>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={isPull} onCheckedChange={setIsPull} /> this is a pull request
        </label>
        <div>
          <Label>Comment / instruction</Label>
          <Input
            value={comment}
            placeholder="@claude please review --focus=security"
            onChange={(e) => setComment(e.target.value)}
          />
        </div>
        <div className="flex items-center justify-between">
          <span className="text-xs text-muted-foreground">{msg}</span>
          <Button variant="outline" onClick={go} disabled={busy || !owner || !repo || !issue || !handle}>
            {busy ? "Invoking…" : "Invoke now"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
