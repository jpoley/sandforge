// Typed client for the sandforge agents JSON API (served by internal/agents/server.go).

export interface Agent {
  handle: string;
  name: string;
  command: string[];
  kind: string;
  on_open: boolean;
  enabled: boolean;
}

export interface EventRec {
  id: string;
  ts: string;
  kind: string; // delivery | route | result
  event: string;
  repo: string;
  issue: number;
  is_pull: boolean;
  handle: string;
  trigger: string;
  sender: string;
  status: string; // queued | running | done | failed | ignored
  detail: string;
  output: string;
  pushed: boolean;
  duration_s: number;
}

export interface Status {
  webhook_url: string;
  forge_url: string;
  secret_set: boolean;
  bot_login: string;
  agent_count: number;
  async: boolean;
  agent_marker: string;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, init);
  if (!r.ok) throw new Error((await r.text()) || `${r.status} ${r.statusText}`);
  const ct = r.headers.get("content-type") || "";
  return (ct.includes("json") ? r.json() : r.text()) as Promise<T>;
}

// API paths are ROOT-absolute ("/api/...") on purpose: the server serves the SPA at the origin
// root and falls back to index.html for any path, so a relative path ("api/status") issued from a
// non-root URL (e.g. "/docs/") would resolve to "/docs/api/status" and return HTML, not JSON.
export const api = {
  status: () => req<Status>("/api/status"),
  agents: () => req<Agent[]>("/api/agents"),
  saveAgent: (a: Partial<Agent>) =>
    req<Agent[]>("/api/agents", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(a),
    }),
  deleteAgent: (handle: string) =>
    req<Agent[]>(`/api/agents/${encodeURIComponent(handle)}`, { method: "DELETE" }),
  events: (n = 100) => req<EventRec[]>(`/api/events?n=${n}`),
  docs: () => req<string>("/api/docs"),
  trigger: (body: {
    owner: string;
    repo: string;
    issue: number;
    is_pull: boolean;
    handle: string;
    comment: string;
  }) =>
    req<EventRec>("/api/trigger", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    }),
};
