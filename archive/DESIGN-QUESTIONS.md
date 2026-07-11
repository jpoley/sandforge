# Sandforge — Design Interrogation

A multi-round grilling of the Sandforge concept (see `README.md`). The goal: surface
**every key design question, decision point, and tradeoff** before a line of code is written.

Three rounds:
- **Round 1 (Claude/Opus)** — the spine, first-order forks.
- **Round 2 (Codex, write access)** — independent adversarial pass.
- **Round 3 (Claude/Opus)** — synthesis + second-order questions that only surface once 1 & 2 are on the table.

Answer inline under each question (`> A:`), or strike the ones you've decided are non-issues.

---

## Round 1 — Claude (Opus): The spine and first-order forks

### 1. Identity — what *is* this? (decide first; everything falls out of it)

The README pitches **isolation** in §1 ("keep agent noise off the team's assembly line")
but builds for **fidelity** in §4 (minikube + Forgejo + PV + Helm). Those are two different
products with two different stacks. Pick one as primary:

- **Isolation play** — just keep agent noise out of the team's git.
- **Fidelity play** — a high-fidelity local replica of the team's PR+CI flow.
- **Curation engine** — the value is purely messy-history → one-clean-PR; the sandbox is incidental.

**Q1.1** Which is the *primary* identity? The other two become secondary, not co-equal.

**Q1.2 (the project-ending question)** Why does a **personal/private fork** on the real
GitHub/GitLab not solve this? Name the specific capability it lacks. A fork gives you: real
PRs, real branch protection, real auto-merge, real CI, complete isolation — at $0 infra.
If you can't name what's missing, the heavy stack is over-engineered.

**Q1.3** If the real pain is *visibility* (40 draft PRs cluttering the org), why isn't the
answer a private fork / separate account / naming convention rather than running Kubernetes
on a laptop?

> A:

### 2. The all-or-nothing coupling: forge ⟹ CI ⟹ runners ⟹ infra

The forge's PR / auto-merge / webhook machinery only adds value **over bare git** if there's
something to gate on — i.e. **CI**. With no CI, "auto-merge" is just `git merge` and a "PR"
is just a branch diff.

**Q2.1** Which forge features does the agent loop *actually* exercise? Push/branch/merge →
bare git already does that. PRs/webhooks/branch-protection/auto-merge-on-green → need the
forge *and* CI to mean anything. List the ones that are real for your workflow.

**Q2.2** Is CI **in or out**? §11 lists Forgejo Actions runners as "future work." I claim
CI is the load-bearing wall: without it, the entire Forgejo+minikube layer is ceremony over
bare git. Confirm or refute.

**Q2.3** If CI is in: do the runners execute the team's *actual* upstream CI config, or a
stand-in? (Fidelity is the only justification for the heavy stack — a fake CI defeats it.)

> A:

### 3. The real product: messy history → one clean PR (least-specified part of the README)

The sandbox is commodity. The value is the *transformation*. §5's `graduate`/`upstream` is
the thinnest part of the doc.

**Q3.1** What is the **diff base** for the "clean" PR? Sandbox branch vs. *what* — the stale
mirror's HEAD, upstream's current HEAD, or the import point?

**Q3.2** Squash, rebase, or cherry-pick? Who curates what lands in the diff — the human, a
preflight hook, or the agent? "One clean PR" is a history-rewriting operation the README
never describes.

**Q3.3 (drift)** The agent built against a **read-only mirror** that's now stale; upstream
moved underneath. Where does rebase-onto-fresh-upstream happen — at `graduate` or `upstream`?
What happens on conflict? Right now the README says the mirror is read-only (§9) but never
says where reconciliation occurs — so it occurs *nowhere*.

> A:

### 4. Substrate — minikube vs. lighter

**Q4.1** Why Kubernetes for a single-developer local Git server? `docker run forgejo` (or
compose) gives the same forge with a fraction of the complexity, RAM, and cold-start time.
The README's justification ("reproducible, declarative, could run remote later") — is "could
run remote later" a real requirement or speculative?

**Q4.2** Resource math: minikube + Forgejo + PV + CI runners, *while simultaneously* running
multiple hungry AI agents on the same laptop. Have you added up the RAM? Agents and sandbox
compete for one machine.

**Q4.3** "Rebuild in minutes" vs. minikube cold-start reality (`minikube start` + Helm +
Forgejo init + token seed). Is the disposability claim honest?

> A:

### 5. Persistence vs. disposability (they conflict)

**Q5.1** §7 has both `persist: true` (keep PV) and §2 promises "disposable, tear down and
rebuild in minutes." When they conflict — in-progress agent work vs. a clean reset — which
wins by default?

**Q5.2** What exactly is worth persisting? The Forgejo DB + repos, or just the curated
branches? Could you persist *only* the git refs (cheap) and treat the forge as disposable?

> A:

### 6. Threat model — malicious agent vs. messy agent

§9's isolation is about the *Forgejo token's scope*. But agents run on your machine with your
shell — they can read `~/.aws/vault/GITHUB`, your kubeconfig, etc.

**Q6.1** Are you defending against a **malicious** agent (then token-scoping is irrelevant;
you need OS/container/VM-level isolation) or a **messy** agent (then token-scoping is fine and
§9 is honest)? Pick one — it changes the whole isolation design.

**Q6.2** The Upstreamer holds the only prod-write credential. If an agent can write files in
the repo it works on, can it plant something that the *human's* `upstream` step then pushes
with prod creds? (Supply-chain-into-yourself.)

> A:

### 7. Build shape — what is sandforge the binary, really?

**Q7.1** Is this a real Go binary reimplementing provisioning, or a thin wrapper over
`minikube` + Helm + `gh`/`tea`? How much of §8's "Provisioner / Deployer / Mirror engine /
Upstreamer" is just shelling out to tools that already exist?

**Q7.2** `upstream` opens a PR via the provider API (GitHub/GitLab/Forgejo). That's three
provider integrations to build and maintain. Could `gh`/`glab`/`tea` do it instead of a
custom Upstreamer?

> A:

---

## Round 2 — Operational / "ships and breaks" pass

> NOTE: This round was meant to run on **Codex with write access**. Codex auth is currently
> broken (ChatGPT account rejects all models — `auth.json` is stale; fix with `codex login`).
> Written by Claude as a stand-in over Codex's intended focus areas. Re-run Codex to get its
> independent voice appended.

### 8. Networking / DNS — the silent first-run killer

**Q8.1** `forge.localhost` (§7) — how does it resolve? minikube's IP is not localhost. Options:
`minikube tunnel` (needs sudo, must stay running), NodePort + port-forward (process must stay
alive), Ingress + `/etc/hosts` edit (sudo, pollutes host), or `*.localhost`→127.0.0.1 tricks
(works in browsers, not in `git`/curl reliably). Each has a different failure mode. Which?

**Q8.2** Whatever URL agents clone (`http://forge.localhost/...`) must be stable across cluster
restarts. NodePorts and tunnel IPs are *not* stable by default. If the URL changes on every
`init`, every agent's existing remote breaks. How is URL stability guaranteed?

**Q8.3** HTTP or HTTPS? Self-signed TLS means every agent/git client needs the cert trusted or
`GIT_SSL_NO_VERIFY`. Plain HTTP means tokens travel in the clear (fine locally, but some git
hosting flows refuse). Pick.

> A:

### 9. Webhooks & auto-merge mechanics inside the cluster

**Q9.1** Auto-merge-on-green requires Forgejo to fire a webhook → CI runner → status back →
merge. Webhook delivery *inside* minikube to a runner pod is its own networking problem
(cluster-internal DNS, the runner registering with Forgejo). Is this wired, or is "auto-merge"
actually unconditional merge with no checks?

**Q9.2** If auto-merge is unconditional, what stops the agent's broken commits from piling up
on the target branch, making the eventual `graduate` diff a swamp? Is there *any* gate, or is
the whole point that the sandbox branch is disposable and only the curated diff matters?

> A:

### 10. Concurrency — multiple agents, one sandbox

**Q10.1** N agents working at once: do they share working clones (§4 "Local working clones")
or each get their own? Shared clones + concurrent `git` = index corruption. Separate clones =
N× disk and N× the stale-mirror problem.

**Q10.2** Branch-name collisions: two agents both create `feature/fix`. Namespacing by agent?
By session? First-writer-wins with force-push? This determines whether agents can run truly in
parallel or must be serialized.

**Q10.3** Is there any locking around `import`/`reset`/`graduate` while agents are mid-push?
A `reset` during an active agent push corrupts state.

> A:

### 11. Mirror correctness — the import is not a one-time `git clone`

**Q11.1** `mirrorInterval > 0` re-syncs upstream. What happens when upstream **force-pushes**
or **deletes a branch** the agent based work on? Hard fail, silent divergence, or detach?

**Q11.2** Real repos have **submodules, LFS, large history**. Does the mirror handle them?
LFS in particular needs separate storage config in Forgejo. A 5GB monorepo mirror also blows
the minikube PV sizing in §7.

**Q11.3** "Read-only mirror" (§9) — Forgejo mirror repos are literally read-only; agents can't
push branches *to the mirror*. So agents push to a *separate* writable repo? Or is the mirror
actually a normal repo seeded from upstream (and "read-only" is just a discipline, not
enforced)? This ambiguity is load-bearing for the whole isolation claim.

> A:

### 12. Bootstrap & secrets

**Q12.1** §6 "seed admin user + token." Forgejo first-run admin creation is non-trivial to
automate idempotently (it differs by version). Where does the token get stored
(`~/.sandforge/credentials`, §7) and with what file perms? Does `reset` rotate it?

**Q12.2** The agent-facing token is local-only (good). But where does the *upstream* prod
credential live, and how do you guarantee it's *never* in an env var visible to a process the
agent spawned? (Ties to Q6.2.)

> A:

### 13. Observability — debugging the silent failure

**Q13.1** An agent does `git push` and it *appears* to succeed but the webhook/CI/auto-merge
silently failed. How does the developer find out? Is there a `sandforge logs` / `sandforge
status` that surfaces forge + cluster + runner health in one place, or do they `kubectl logs`
by hand? First-run debuggability is what makes or breaks local-dev tooling adoption.

**Q13.2** When `init` fails halfway (minikube up, Forgejo crash-looping), is there a clean
recovery, or is the only path `down` + retry? Partial-failure UX is where these tools die.

> A:

### 14. `graduate` preflight — where and what

**Q14.1** §7 preflight runs `make test`, etc. **In what environment?** On the host (needs the
host toolchain), in a cluster pod (needs the image), or in the agent's clone? Each gives
different results and different "works on my machine" gaps vs. real upstream CI.

**Q14.2** If preflight passes locally but upstream CI later fails, the whole "transfers cleanly"
promise breaks. Is preflight meant to *be* upstream CI, or just a cheap pre-filter?

> A:

### 15. Portability & versioning

**Q15.1** minikube drivers differ across macOS (hyperkit/qemu/docker), Linux (docker/kvm),
Windows (hyperv/wsl2). The "one command" promise (§2) has very different reliability per
platform. Which platforms are actually supported v1?

**Q15.2** Forgejo `version: "latest"` (§7) means a `reset` weeks later may pull a new Forgejo
that breaks your manifests/bootstrap. Pin, or float? Floating = reproducibility lie; pinning =
maintenance burden.

> A:

### 16. Competitive landscape — has someone already built this?

**Q16.1** Why not `git worktree` + local bare repos (zero infra, handles 90% of "isolated
iteration")? What does Sandforge add over that for the *non*-PR-needing case?

**Q16.2** Why not existing tools: **Gitpod/devcontainers** (ephemeral dev envs), **gitea +
act_runner** via plain docker-compose (same forge, no k8s), **soft-serve/charm** (dead-simple
self-host git), **Gerrit** (built for exactly the "curate before upstream" model)? Sandforge
should be able to say in one sentence why each is wrong for this. Can it?

> A:

---

## Round 3 — Synthesis: second-order questions + the forced decision order

Rounds 1–2 each attack a surface. Round 3 is about the questions that only appear once you see
them together, plus the **order** decisions must be made in (later ones are wasted if earlier
ones flip).

### 17. The contradiction at the center

Stack Q1.2 (a fork solves isolation for free) against Q2.2 (only CI justifies the heavy stack)
against Q14.2 (preflight must *be* upstream CI to deliver "transfers cleanly").

**Q17.1** Follow the chain: to justify Sandforge over a fork, you need fidelity; fidelity means
real CI; real CI means running the team's actual pipeline locally on a laptop while agents also
run. **Is that physically/operationally viable, or does the justification eat itself?** If the
only honest answer is "preflight is a cheap pre-filter, not real CI," then by Q1.2 a fork wins
and Sandforge has no reason to exist. This is the single make-or-break.

**Q17.2** Is there a *fourth* identity the README didn't name: **a curation/teaching harness**
where the value is neither isolation nor fidelity but giving agents a forge-shaped target so
their PR-opening / review-responding behavior has somewhere to *practice* that you can throw
away? If so, fidelity-of-CI stops mattering and the stack can be light. Is that the real thing?

> A:

### 18. What is the actual unit of value shipped?

**Q18.1** The output is "one clean PR." Strip everything else: could Sandforge be a **single
command that takes a pile of agent commits/branches and produces one curated PR against
upstream** — with *no* persistent forge at all (agents work in worktrees, the tool does the
history surgery + PR open)? What specifically is lost? If little is lost, §4's entire cluster
is the wrong altitude.

**Q18.2** Conversely, if the forge *is* essential because agents need to *open PRs and respond
to review* as part of their loop (not just produce commits), say that explicitly — it's a much
stronger justification than "isolation" and it reframes the whole README.

> A:

### 19. Lifecycle gaps the state machine hides (§5)

**Q19.1** The state diagram has no **abandoned-but-not-reset** state. Real usage: 30 dead
branches accumulate. Is there GC? Or does the sandbox rot until a full `reset`?

**Q19.2** No **multi-graduation**: two good branches from one sandbox, both worth upstreaming,
possibly interdependent. Does `upstream` handle a stack/series, or one branch at a time?

**Q19.3** What happens *after* `upstream`? The PR gets review comments upstream. Does the
developer pull those back into the sandbox to iterate (round-trip), or is the sandbox abandoned
once upstreamed? The README treats `upstream` as terminal (`Upstreamed --> [*]`), but real PRs
are not fire-and-forget.

> A:

### 20. Success criteria — how do we know it worked?

**Q20.1** What's the testable acceptance criterion for Sandforge v1? E.g. "an agent can clone,
push 10 branches, open 5 PRs, auto-merge 3, and the dev upstreams 1 clean PR — total host RAM
< X, cold start < Y." Without a number on RAM and cold-start, Q4.2/Q4.3 stay vibes.

**Q20.2** What's the *kill criterion*? If cold start is >10 min or steady-state RAM >Z with two
agents running, is the k8s approach abandoned for docker-compose? Name the threshold now, before
sunk cost sets in.

> A:

## Round 2-prime — Codex (gpt-5.3-codex-spark): independent adversarial pass

### 21. Execution substrate — DinD runner vs host runner

**Q21.1** Do we force CI to run via `act_runner` with Docker-in-Docker (CI-step parity, but heavy startup, nested-Docker fragility, and noisy scheduling), or run workflows on a host runner (lower overhead and better caching control, but weaker parity and weaker sandboxing)? If the vision is “fully local, inner-loop CI,” what is the acceptable failure mode when a repo needs privileged/container actions?

> A:

### 22. Caching + network isolation — deterministic speed vs hidden nondeterminism

**Q22.1** Do we share a long-lived cache volume across workflows (fast repeat runs and realistic e2e reuse, but cross-branch cache poisoning + unbounded PV growth), or flush caches per run/branch (slower but deterministic, cleaner cleanup)? What policy ties cache invalidation to lockfile/hash changes so stale artifacts don’t make stale green checks look trustworthy?

> A:

### 23. Workspace topology — shared mirror vs per-agent worktrees

**Q23.1** Should all N agents work against a shared writable Forgejo repo mirror plus separate Git worktrees (less I/O, faster branching, but index/worktree contention and harder cleanup), or isolated full clones per agent (clean semantics, but 1 repo → N× disk/IO and worse startup under large histories)? What is the hard kill-switch if a daily-driver machine hits disk pressure mid-run?

> A:

### 24. Secrets and action context — “no prod creds in agent space” vs practical ergonomics

**Q24.1** Do we enforce a hard split where CI runners and agent shells never read any upstream credential (safe by design, but hard to operate), or allow ephemeral convenience surfaces like agent-loaded env files/`gh auth` (faster iteration, but credential bleed risk from any agent-run script)? Which controls prove that "upstream credentials reaching local CI/agent context" is impossible rather than accidental.

> A:

### 25. PRD success criteria — checklist text vs executable contract

**Q25.1** Is PRD-validation an auditable, machine-enforced gate (e.g., explicit tests/acceptance files bound to a PRD schema, versioned and failing gate on mismatch), or a manual PR narrative (fast to ship but unverifiable at scale)? If it is a contract, where does the contract live and how do we pin/approve changes without turning the gate itself into tribal debt?

> A:

### 26. Auto-promotion logic — fast throughput vs flake correctness

**Q26.1** Does `staging→main` promotion run on strict deterministic criteria (single-source truth and quorum gating, but slower and more fragile to transient infra churn), or probabilistic "first green after retry" behavior (faster but can publish flaky or time-sensitive results)? What is the rollback story when local e2e flakes despite green local history under identical inputs?

> A:

### 27. Agent handoff state — Git-only coordination vs shared session state

**Q27.1** Is state between models/agents conveyed only through git artifacts and branch naming (zero infra, naturally auditable, but loses fine-grained intent/results metadata), or via shared task/state storage (explicit intent transfer and richer context, but new schema + migration + conflict surface)? If only git is used, how does B know what A tested, failed, or skipped in a way that is hard to lose.

> A:

### 28. Performance claim — “faster than GitHub” where do we measure?

**Q28.1** Do we benchmark “inner-loop latency” (clone/push/check/review loop, where local can win) at the expense of occasional long rebuilds and contention, or “time-to-first-truly-green PRD gate” (where local CI parity often loses without enough CPUs/IO)? Which metric is the contract, and what is the fallback when local cluster pressure from the host causes a slower PRD gate than upstream CI on peak days?

> A:

### 29. Operational drag on a daily-driver laptop — always-on cluster vs on-demand

**Q29.1** Do we keep CI/CD services warm all day (instant responses, but predictable RAM/CPU tax and port contention with other developer workloads), or spin everything up lazily per command (lower baseline cost, but slower first run and brittle one-command assumptions when Docker is busy with other stacks)? Which ports/services must be guaranteed conflict-free on a machine already running Docker, browsers, DBs, and local emulators?

> A:

---

## The forced decision order (resolve top-down; each gates the rest)

1. **Identity (Q1.1) + fork-justification (Q1.2 / Q17.1).** If a fork wins, stop — build the
   curation tool (Q18.1), not the cluster.
2. **CI in/out (Q2.2).** Out ⟹ drop Forgejo+minikube, go bare-git/worktrees + curation tool.
   In ⟹ accept the infra and continue.
3. **Substrate (Q4.x).** Only meaningful if CI is in: minikube vs. docker-compose. Set the
   RAM/cold-start kill thresholds (Q20.2) *here*.
4. **Curation mechanics + drift (Q3.x).** The actual product; specify diff-base, history-rewrite
   strategy, and where rebase-onto-fresh-upstream happens.
5. **Mirror model + isolation enforcement (Q11.3, Q6.1).** Decide if "read-only mirror" is
   enforced or disciplinary; pick the threat model.
6. **Everything operational (Round 2: networking, concurrency, bootstrap, observability).** Only
   worth solving once 1–5 are locked.

> Decisions 4–6 are wasted effort if 1–3 flip. Do not let agents (or yourself) start building at
> layer 6 (the fun Kubernetes part) before layers 1–3 are answered in writing.
