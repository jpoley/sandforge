import { useCallback, useEffect, useState } from "react";
import {
  Hammer,
  Moon,
  Sun,
  ShieldCheck,
  ShieldAlert,
  LayoutDashboard,
  BookOpen,
  ExternalLink,
} from "lucide-react";
import { api, type Agent, type EventRec, type Status } from "@/lib/api";
import { Badge, Button, buttonVariants } from "@/components/ui";
import { cn } from "@/lib/utils";
import { AgentsPanel } from "@/components/AgentsPanel";
import { Timeline } from "@/components/Timeline";
import { TriggerPanel } from "@/components/TriggerPanel";
import { Docs } from "@/components/Docs";

type Tab = "dashboard" | "docs";

function useTheme() {
  const [dark, setDark] = useState(() => {
    const s = localStorage.getItem("sf-theme");
    if (s) return s === "dark";
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? true;
  });
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("sf-theme", dark ? "dark" : "light");
  }, [dark]);
  return { dark, toggle: () => setDark((d) => !d) };
}

export default function App() {
  const { dark, toggle } = useTheme();
  const [tab, setTab] = useState<Tab>("dashboard");
  const [agents, setAgents] = useState<Agent[]>([]);
  const [events, setEvents] = useState<EventRec[]>([]);
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState("");

  const loadAgents = useCallback(() => {
    api.agents().then(setAgents).catch((e) => setError(String(e.message)));
  }, []);
  const loadEvents = useCallback(() => {
    api.events(80).then(setEvents).catch(() => {});
  }, []);
  const loadStatus = useCallback(() => {
    api.status().then(setStatus).catch(() => {});
  }, []);

  useEffect(() => {
    loadAgents();
    loadStatus();
    loadEvents();
    const t = setInterval(loadEvents, 3000);
    return () => clearInterval(t);
  }, [loadAgents, loadStatus, loadEvents]);

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-10 border-b bg-card/80 backdrop-blur">
        <div className="mx-auto flex max-w-7xl items-center gap-3 px-6 py-3">
          <div className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
            <Hammer className="h-4 w-4" />
          </div>
          <div className="mr-auto">
            <h1 className="text-sm font-semibold leading-tight">Sandforge · Local Agents</h1>
            <p className="text-xs text-muted-foreground">a local Copilot — route Forgejo @mentions to coding agents</p>
          </div>

          <nav className="hidden items-center gap-1 rounded-md border bg-background p-0.5 sm:flex">
            <button
              onClick={() => setTab("dashboard")}
              className={cn(
                "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors",
                tab === "dashboard" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
              )}
            >
              <LayoutDashboard className="h-3.5 w-3.5" /> Dashboard
            </button>
            <button
              onClick={() => setTab("docs")}
              className={cn(
                "inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors",
                tab === "docs" ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
              )}
            >
              <BookOpen className="h-3.5 w-3.5" /> Docs
            </button>
          </nav>

          {status?.forge_url && (
            <a
              href={status.forge_url}
              target="_blank"
              rel="noreferrer"
              className={cn(buttonVariants({ variant: "outline", size: "sm" }), "gap-1.5")}
            >
              <ExternalLink className="h-3.5 w-3.5" /> Forgejo
            </a>
          )}
          {status && (
            <Badge variant={status.secret_set ? "success" : "warning"} className="hidden gap-1 md:inline-flex">
              {status.secret_set ? <ShieldCheck className="h-3 w-3" /> : <ShieldAlert className="h-3 w-3" />}
              {status.secret_set ? "secured" : "no secret"}
            </Badge>
          )}
          <Button size="icon" variant="ghost" onClick={toggle} aria-label="Toggle theme">
            {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
          </Button>
        </div>
      </header>

      {tab === "docs" ? (
        <main className="mx-auto max-w-4xl px-6 py-6">
          <Docs />
        </main>
      ) : (
        <main className="mx-auto grid max-w-7xl gap-5 px-6 py-6 lg:grid-cols-[minmax(0,420px)_1fr]">
          <div className="space-y-5">
            {status && (
              <div className="rounded-lg border bg-card px-4 py-3 text-xs text-muted-foreground">
                <span className="font-medium text-foreground">Webhook</span>{" "}
                <code className="break-all font-mono">{status.webhook_url || "(not registered)"}</code>
              </div>
            )}
            {error && (
              <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
                {error}
              </div>
            )}
            <AgentsPanel agents={agents} reload={loadAgents} />
            <TriggerPanel agents={agents} onDone={loadEvents} />
          </div>
          <Timeline events={events} reload={loadEvents} />
        </main>
      )}
    </div>
  );
}
