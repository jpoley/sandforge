import { useState } from "react";
import { Bot, Pencil, Plus, Trash2 } from "lucide-react";
import { api, type Agent } from "@/lib/api";
import { parseArgv } from "@/lib/utils";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Input, Label, Switch, Textarea } from "./ui";
import { Dialog } from "./Dialog";

const EMPTY: Agent = { handle: "", name: "", command: [], kind: "review", on_open: false, enabled: true };

export function AgentsPanel({ agents, reload }: { agents: Agent[]; reload: () => void }) {
  const [editing, setEditing] = useState<Agent | null>(null);
  const [cmd, setCmd] = useState("");
  const [err, setErr] = useState("");

  function open(a: Agent) {
    setEditing({ ...a });
    setCmd(a.command.join(" "));
    setErr("");
  }
  async function save() {
    if (!editing) return;
    try {
      await api.saveAgent({ ...editing, command: parseArgv(cmd) });
      setEditing(null);
      reload();
    } catch (e) {
      setErr(String((e as Error).message));
    }
  }
  async function remove(handle: string) {
    if (!confirm(`Delete @${handle}?`)) return;
    await api.deleteAgent(handle);
    reload();
  }
  async function toggle(a: Agent) {
    await api.saveAgent({ ...a, enabled: !a.enabled });
    reload();
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2">
          <Bot className="h-4 w-4 text-primary" /> Agents
        </CardTitle>
        <Button size="sm" onClick={() => open(EMPTY)}>
          <Plus className="h-4 w-4" /> Add agent
        </Button>
      </CardHeader>
      <CardContent className="space-y-2">
        {agents.length === 0 && <p className="text-sm text-muted-foreground">No agents configured yet.</p>}
        {agents.map((a) => (
          <div
            key={a.handle}
            className="flex items-start justify-between gap-3 rounded-md border bg-background/40 p-3"
          >
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold text-primary">@{a.handle}</span>
                <Badge variant="outline">{a.kind}</Badge>
                {a.on_open && <Badge variant="accent">on-open</Badge>}
                <Badge variant={a.enabled ? "success" : "default"}>{a.enabled ? "enabled" : "disabled"}</Badge>
              </div>
              <code className="mt-1 block truncate font-mono text-xs text-muted-foreground">
                {a.command.join(" ") || "(no command)"}
              </code>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              <Switch checked={a.enabled} onCheckedChange={() => toggle(a)} />
              <Button size="icon" variant="ghost" onClick={() => open(a)} aria-label="Edit">
                <Pencil className="h-4 w-4" />
              </Button>
              <Button size="icon" variant="ghost" onClick={() => remove(a.handle)} aria-label="Delete">
                <Trash2 className="h-4 w-4 text-destructive" />
              </Button>
            </div>
          </div>
        ))}
      </CardContent>

      <Dialog
        open={!!editing}
        onClose={() => setEditing(null)}
        title={editing?.handle ? `Edit @${editing.handle}` : "Add agent"}
        description="Map a mention handle to a command. It runs in a fresh clone of the repo; its output is posted back as a review."
      >
        {editing && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label>Handle (without @)</Label>
                <Input
                  value={editing.handle}
                  placeholder="claude"
                  onChange={(e) => setEditing({ ...editing, handle: e.target.value })}
                />
              </div>
              <div>
                <Label>Name</Label>
                <Input
                  value={editing.name}
                  placeholder="Claude"
                  onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                />
              </div>
            </div>
            <div>
              <Label>Kind</Label>
              <select
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                value={editing.kind}
                onChange={(e) => setEditing({ ...editing, kind: e.target.value })}
              >
                <option value="review">review</option>
                <option value="implement">implement</option>
              </select>
            </div>
            <div>
              <Label>Command (argv, space-separated)</Label>
              <Textarea value={cmd} placeholder={`claude -p "review this PR"`} onChange={(e) => setCmd(e.target.value)} />
              <p className="mt-1 text-xs text-muted-foreground">
                Env available: $SANDFORGE_REPO, $SANDFORGE_ISSUE, $SANDFORGE_BRANCH, $SANDFORGE_COMMENT, $SANDFORGE_TOKEN.
              </p>
            </div>
            <div className="flex items-center gap-6">
              <label className="flex items-center gap-2 text-sm">
                <Switch
                  checked={editing.on_open}
                  onCheckedChange={(v) => setEditing({ ...editing, on_open: v })}
                />
                auto-run on PR open
              </label>
              <label className="flex items-center gap-2 text-sm">
                <Switch
                  checked={editing.enabled}
                  onCheckedChange={(v) => setEditing({ ...editing, enabled: v })}
                />
                enabled
              </label>
            </div>
            {err && <p className="text-sm text-destructive">{err}</p>}
            <div className="flex justify-end gap-2 pt-1">
              <Button variant="outline" onClick={() => setEditing(null)}>
                Cancel
              </Button>
              <Button onClick={save}>Save agent</Button>
            </div>
          </div>
        )}
      </Dialog>
    </Card>
  );
}
