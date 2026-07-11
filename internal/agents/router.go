package agents

import (
	"fmt"
	"strings"
	"time"
)

// Job is a single agent invocation request handed to the Dispatcher. The dispatcher owns the
// side-effects (clone the repo at the right ref, run the agent command, post the comment back,
// push any commits) because those need host git + the forge client, which the router stays free of.
type Job struct {
	Owner       string
	Repo        string
	IssueNumber int
	IsPull      bool
	Branch      string // PR head ref when known from the payload; else dispatcher resolves it
	Handle      string
	Agent       Agent
	Comment     string // the triggering comment body (the instruction to the agent)
	Trigger     string // mention | manual | on_open
	Sender      string
}

// Result is what a Dispatcher reports back.
type Result struct {
	Output   string
	Pushed   bool
	Err      error
	Duration time.Duration
}

// Dispatcher invokes one agent. Implemented in the app layer (it needs git + the forge client).
type Dispatcher interface {
	Dispatch(Job) Result
}

// Router turns a verified delivery into agent Jobs, persists the timeline, and runs the dispatcher.
type Router struct {
	Cfg        *Config
	Store      *Store
	Dispatcher Dispatcher
	// Async, when set, runs each dispatch in a goroutine so the webhook handler can ack fast.
	Async bool
}

// HandleDelivery verifies + parses a delivery and routes any mention triggers. Returns the route
// events it created (a delivery is always recorded). Verification failure is a loud error.
func (r *Router) HandleDelivery(eventType string, body []byte, sigHeader string) ([]Event, error) {
	if err := VerifySignature(r.Cfg.Secret, body, sigHeader); err != nil {
		return nil, err
	}
	p, err := ParsePayload(body)
	if err != nil {
		return nil, err
	}
	repo := p.Repository.Owner.Login + "/" + p.Repository.Name

	switch eventType {
	case "issue_comment":
		return r.routeComment(p, repo), nil
	case "pull_request":
		return r.routePullRequest(p, repo), nil
	default:
		r.Store.Append(Event{Kind: "delivery", Event: eventType, Repo: repo, Status: StatusIgnored,
			Detail: "event type not handled"})
		return nil, nil
	}
}

func (r *Router) routeComment(p *Payload, repo string) []Event {
	base := Event{Kind: "delivery", Event: "issue_comment", Repo: repo, Issue: p.Issue.Number,
		IsPull: p.IsPull, Sender: p.Sender.Login}
	// Only act on freshly created comments.
	if p.Action != "created" {
		base.Status = StatusIgnored
		base.Detail = "action=" + p.Action
		r.Store.Append(base)
		return nil
	}
	// Loop guard: never react to an agent's own comment, or to the configured bot login.
	if strings.Contains(p.Comment.Body, AgentMarker) ||
		(r.Cfg.BotLogin != "" && strings.EqualFold(p.Comment.User.Login, r.Cfg.BotLogin)) {
		base.Status = StatusIgnored
		base.Detail = "loop-guard: agent/bot-authored comment"
		r.Store.Append(base)
		return nil
	}
	mentions := Mentions(p.Comment.Body)
	if len(mentions) == 0 {
		base.Status = StatusIgnored
		base.Detail = "no @mention"
		r.Store.Append(base)
		return nil
	}
	// A delivery with one or more @mentions still needs an explicit status so the UI doesn't render a blank state.
	base.Status = StatusReceived
	base.Detail = "@" + strings.Join(mentions, " @")
	r.Store.Append(base)
	var routed []Event
	matched := false
	for _, h := range mentions {
		ag := r.Cfg.FindAgent(h)
		if ag == nil {
			continue
		}
		matched = true
		branch := ""
		if p.PullRequest != nil {
			branch = p.PullRequest.Head.Ref
		}
		job := Job{
			Owner: p.Repository.Owner.Login, Repo: p.Repository.Name,
			IssueNumber: p.Issue.Number, IsPull: p.IsPull, Branch: branch,
			Handle: ag.Handle, Agent: *ag, Comment: p.Comment.Body,
			Trigger: "mention", Sender: p.Sender.Login,
		}
		routed = append(routed, r.run(job))
	}
	if !matched {
		r.Store.Append(Event{Kind: "route", Event: "issue_comment", Repo: repo, Issue: p.Issue.Number,
			Status: StatusIgnored, Detail: "mentions " + strings.Join(mentions, ",") + " match no configured agent"})
	}
	return routed
}

func (r *Router) routePullRequest(p *Payload, repo string) []Event {
	act := map[string]bool{"opened": true, "synchronize": true, "synchronized": true, "reopened": true}
	if !act[p.Action] || p.PullRequest == nil {
		r.Store.Append(Event{Kind: "delivery", Event: "pull_request", Repo: repo, IsPull: true,
			Status: StatusIgnored, Detail: "action=" + p.Action})
		return nil
	}
	r.Store.Append(Event{Kind: "delivery", Event: "pull_request", Repo: repo, Issue: p.PullRequest.Number,
		IsPull: true, Sender: p.Sender.Login, Status: StatusReceived, Detail: "action=" + p.Action})
	var routed []Event
	for _, ag := range r.Cfg.Snapshot() {
		if !ag.Enabled || !ag.OnOpen {
			continue
		}
		job := Job{
			Owner: p.Repository.Owner.Login, Repo: p.Repository.Name,
			IssueNumber: p.PullRequest.Number, IsPull: true, Branch: p.PullRequest.Head.Ref,
			Handle: ag.Handle, Agent: ag,
			Comment: fmt.Sprintf("PR #%d %s — auto-review on %s", p.PullRequest.Number, p.PullRequest.Title, p.Action),
			Trigger: "on_open", Sender: p.Sender.Login,
		}
		routed = append(routed, r.run(job))
	}
	return routed
}

// Trigger runs a job built outside the webhook path (the manual "invoke" button / CLI). The agent
// is looked up by handle; an unknown handle is a loud error, not a silent no-op.
func (r *Router) Trigger(owner, repo string, issue int, isPull bool, handle, comment string) (Event, error) {
	ag := r.Cfg.FindAgent(handle)
	if ag == nil {
		return Event{}, fmt.Errorf("no enabled agent with handle %q", handle)
	}
	if comment == "" {
		comment = fmt.Sprintf("manual trigger: @%s on %s/%s#%d", ag.Handle, owner, repo, issue)
	}
	job := Job{
		Owner: owner, Repo: repo, IssueNumber: issue, IsPull: isPull,
		Handle: ag.Handle, Agent: *ag, Comment: comment, Trigger: "manual", Sender: "manual",
	}
	return r.run(job), nil
}

// run records the queued route, dispatches (sync or async), and records the result.
func (r *Router) run(job Job) Event {
	q := r.Store.Append(Event{
		Kind: "route", Event: job.Trigger, Repo: job.Owner + "/" + job.Repo, Issue: job.IssueNumber,
		IsPull: job.IsPull, Handle: job.Handle, Trigger: job.Trigger, Sender: job.Sender,
		Status: StatusQueued, Detail: "dispatching @" + job.Handle,
	})
	exec := func() Event {
		res := r.Dispatcher.Dispatch(job)
		status := StatusDone
		detail := "agent @" + job.Handle + " completed"
		if res.Err != nil {
			status = StatusFailed
			detail = res.Err.Error()
		}
		return r.Store.Append(Event{
			Kind: "result", Event: job.Trigger, Repo: job.Owner + "/" + job.Repo, Issue: job.IssueNumber,
			IsPull: job.IsPull, Handle: job.Handle, Trigger: job.Trigger, Sender: job.Sender,
			Status: status, Detail: detail, Output: res.Output, Pushed: res.Pushed,
			Duration: res.Duration.Seconds(),
		})
	}
	if r.Async {
		go exec()
		return q
	}
	return exec()
}
