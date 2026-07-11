// Command sandforge is the developer's control plane for the inner-loop SDLC forge (docs/goal.md).
// Agents never call this — they just `git` against the forge. The CLI is the human's surface.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jpoley/sandforge/internal/app"
)

const usage = `sandforge — one-command local inner-loop SDLC forge

USAGE:
  sandforge <command> [args]

COMMANDS:
  init                       Bring up the control plane (forge + Postgres + warm runner). Idempotent.
  import <url> [name]        Seed a writable repo from upstream; keep upstream as a remote.
  status                     One-screen instance summary.
  logs [service] [-f]        Tail control-plane (or a service's) logs.
  graduate <repo> <branch>   Rebase onto upstream, spin deploy-target, run e2e + PRD validation.
  upstream <repo> <branch>   Graduate, then open the upstream PR (only prod-touching cmd).
  e2e [--no-playwright]      Stand it up and run the WHOLE closed loop, validating every AC.
  agents <subcommand>        Local "Copilot": route Forgejo @mentions to coding agents (see below).
  reset                      Wipe instance state + rotate credentials.
  down [--keep]              Tear down the control plane (optionally keep volumes).

AGENTS (emulate GitHub Copilot locally — subscribe to Forgejo events, route to @claude/@codex):
  agents up [--port N] [--repo owner/name]      Run the router as a CONTAINER on the control-plane network.
  agents down                                   Tear down the containerized router.
  agents start [--port N] [--repo owner/name]   Start the always-on background host daemon (restarted by init).
  agents stop                                   Stop the daemon and disable always-on routing.
  agents status                                 Show whether the daemon is running + where.
  agents serve [--port N] [--repo owner/name]   Run the listener + web UI in the FOREGROUND (Ctrl-C to stop).
  agents list                                   List configured agents.
  agents add <handle> [--name N] [--kind K] [--on-open] -- <command...>   Add/replace an agent.
  agents trigger <repo> <issue#> <handle> [--pull] [-m msg]   Invoke an agent now (manual handoff).

Defaults are embedded and sensible (docs/goal.md); override via sandforge.yaml or SANDFORGE_* env.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	a, err := app.New(false)
	if err != nil {
		die(err)
	}

	switch cmd {
	case "init":
		die(a.Init())
	case "import":
		if len(args) < 1 {
			die(fmt.Errorf("usage: sandforge import <url> [name]"))
		}
		name := ""
		if len(args) >= 2 {
			name = args[1]
		}
		die(a.Import(args[0], name))
	case "status":
		die(a.Status())
	case "logs":
		fs := flag.NewFlagSet("logs", flag.ExitOnError)
		follow := fs.Bool("f", false, "follow")
		fs.Parse(args)
		svc := ""
		if fs.NArg() > 0 {
			svc = fs.Arg(0)
		}
		die(a.Logs(svc, *follow))
	case "graduate":
		if len(args) < 2 {
			die(fmt.Errorf("usage: sandforge graduate <repo> <branch>"))
		}
		gr, err := a.Graduate(args[0], args[1], "", nil, nil)
		if err != nil {
			die(err)
		}
		fmt.Print(gr.PRBody)
		if !gr.Report.Passed() {
			os.Exit(1)
		}
	case "upstream":
		// Production-touching: graduate (rebase + e2e + PRD gates) then open the upstream PR. The
		// gates block the PR on any failed staging-main criterion, so this never ships un-validated.
		if len(args) < 2 {
			die(fmt.Errorf("usage: sandforge upstream <repo> <branch>"))
		}
		gr, err := a.Graduate(args[0], args[1], "", nil, nil)
		if err != nil {
			die(err)
		}
		if !gr.Report.Passed() {
			fmt.Print(gr.PRBody)
			die(fmt.Errorf("upstream blocked: %d staging-main gate(s) failed", len(gr.Report.BlockingFails)))
		}
		pr, err := a.Upstream(args[0], gr)
		if err != nil {
			die(err)
		}
		if pr.Number > 0 {
			fmt.Printf("opened upstream PR #%d: %s\n", pr.Number, pr.URL)
		} else {
			fmt.Printf("opened upstream PR: %s\n", pr.URL)
		}
	case "e2e":
		fs := flag.NewFlagSet("e2e", flag.ExitOnError)
		noPW := fs.Bool("no-playwright", false, "skip Playwright UI criteria (SC-6/SC-7)")
		fs.Parse(args)
		ok, err := a.E2E(!*noPW)
		if err != nil {
			die(err)
		}
		if !ok {
			os.Exit(1)
		}
	case "agents":
		die(runAgents(a, args))
	case "reset":
		die(a.Reset())
	case "down":
		fs := flag.NewFlagSet("down", flag.ExitOnError)
		keep := fs.Bool("keep", false, "keep volumes")
		fs.Parse(args)
		die(a.Down(*keep))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Printf("unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// runAgents dispatches the `sandforge agents <subcommand>` group (the local-Copilot router).
func runAgents(a *app.App, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: sandforge agents <serve|list|add|trigger> [args]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "serve":
		fs := flag.NewFlagSet("agents serve", flag.ExitOnError)
		port := fs.Int("port", 0, "listener/UI port (default 3999)")
		repo := fs.String("repo", "", "limit to one repo (owner/name); default = all imported repos")
		fs.Parse(rest)
		return a.AgentsServe(*port, *repo)
	case "start":
		fs := flag.NewFlagSet("agents start", flag.ExitOnError)
		port := fs.Int("port", 0, "listener/UI port (default 3999)")
		repo := fs.String("repo", "", "limit to one repo (owner/name); default = all imported repos")
		fs.Parse(rest)
		return a.AgentsStart(*port, *repo)
	case "stop":
		return a.AgentsStop()
	case "status":
		return a.AgentsDaemonStatus()
	case "up":
		fs := flag.NewFlagSet("agents up", flag.ExitOnError)
		port := fs.Int("port", 0, "listener/UI port (default 3999)")
		repo := fs.String("repo", "", "limit to one repo (owner/name); default = all imported repos")
		fs.Parse(rest)
		return a.AgentsUp(*port, *repo)
	case "down":
		return a.AgentsDown()
	case "list":
		return a.AgentsList()
	case "add":
		// Contract: agents add <handle> [flags] -- <command...>. The Go flag package stops at the
		// first positional, so split out the handle + the `--`-delimited command ourselves, then
		// parse the flags that sit between them.
		if len(rest) < 1 {
			return fmt.Errorf("usage: sandforge agents add <handle> [--name N] [--kind K] [--on-open] -- <command...>")
		}
		handle := rest[0]
		mid, command := rest[1:], []string(nil)
		for i, tok := range mid {
			if tok == "--" {
				command = mid[i+1:]
				mid = mid[:i]
				break
			}
		}
		if len(command) == 0 {
			return fmt.Errorf("missing command — put the agent command after `--`, e.g.\n  sandforge agents add %s -- claude -p \"review this\"", handle)
		}
		fs := flag.NewFlagSet("agents add", flag.ExitOnError)
		name := fs.String("name", "", "display name")
		kind := fs.String("kind", "review", "review|implement")
		onOpen := fs.Bool("on-open", false, "also auto-run on PR open/sync")
		fs.Parse(mid)
		return a.AgentsAdd(handle, *name, *kind, *onOpen, command)
	case "trigger":
		// The three positionals (repo, issue, handle) come first; flags ([--pull] [-m]) follow. The
		// Go flag package stops at the first positional, so consume the positionals before parsing.
		if len(rest) < 3 {
			return fmt.Errorf("usage: sandforge agents trigger <repo> <issue#> <handle> [--pull] [-m msg]")
		}
		var issue int
		if _, err := fmt.Sscanf(rest[1], "%d", &issue); err != nil || issue <= 0 {
			return fmt.Errorf("issue number must be a positive integer, got %q", rest[1])
		}
		fs := flag.NewFlagSet("agents trigger", flag.ExitOnError)
		pull := fs.Bool("pull", false, "the issue number is a pull request")
		msg := fs.String("m", "", "comment/instruction to pass the agent")
		fs.Parse(rest[3:])
		return a.AgentsTrigger(rest[0], issue, rest[2], *pull, *msg)
	default:
		return fmt.Errorf("unknown agents subcommand %q (up|down|start|stop|status|serve|list|add|trigger)", sub)
	}
}

func die(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
