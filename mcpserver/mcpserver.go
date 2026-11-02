// Package mcpserver serves a Lagrange workspace to MCP clients (Claude Code, Cursor,
// Claude Desktop, …): list and call addresses, ask people and wait for their answers,
// answer what waits on you, and look up runs. It calls the server's API with a personal
// API key, so everything happens as that person, in the key's workspace, within its role.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/three-agent-problem/lagrange/client"
)

const (
	defaultSendWait = 60 * time.Second
	defaultAskWait  = 120 * time.Second
	maxWait         = 10 * time.Minute
)

// Options tune the server; the zero value is fine.
type Options struct {
	// Poll is how often waiting tools check a task; zero means one second.
	Poll time.Duration
}

type server struct {
	c    *client.Client
	poll time.Duration
}

// New returns an MCP server for the workspace c's key acts in.
func New(c *client.Client, version string, opts Options) *mcp.Server {
	s := &server{c: c, poll: opts.Poll}
	if s.poll <= 0 {
		s.poll = time.Second
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "lagrange", Title: "Lagrange", Version: version}, &mcp.ServerOptions{
		Instructions: "Lagrange gives every agent and person an address (@handle) and an inbox. " +
			"Use list_agents to see who you can reach, send_task to give an agent work, and ask_human to ask a person " +
			"(an approval, a decision, missing information) and wait for their answer. People may take minutes or hours: " +
			"when a tool says it is still waiting, call get_task with the task ID later instead of asking again.",
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_agents", Title: "List addresses", Annotations: readOnly,
		Description: "List the agents and people this workspace can reach, with their handles, whether they are online, and what they do."}, s.listAgents)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_agent_card", Title: "Get an agent card", Annotations: readOnly,
		Description: "Get an address's A2A agent card: its skills, input and output modes, and description."}, s.agentCard)
	mcp.AddTool(srv, &mcp.Tool{Name: "send_task", Title: "Send a task",
		Description: "Send a message to an agent (or person) as a new task, or into an existing task with taskId. " +
			"Waits up to waitSeconds (default 60) for it to finish or to need input, then returns its state and answer."}, s.sendTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "ask_human", Title: "Ask a person",
		Description: "Ask a person a question in their Lagrange inbox (they also get email or Slack, with reminders and escalation " +
			"if the workspace set them up) and wait up to waitSeconds (default 120) for the answer. If they haven't answered yet, " +
			"it returns the task ID: call get_task later."}, s.askHuman)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_task", Title: "Get a task", Annotations: readOnly,
		Description: "Get a task's current state and answer, for example to check on an ask_human or send_task that was still waiting."}, s.getTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_waiting", Title: "List what's waiting", Annotations: readOnly,
		Description: "List the workspace's tasks that wait for an answer, longest first. forYou marks the ones waiting on you."}, s.listWaiting)
	mcp.AddTool(srv, &mcp.Tool{Name: "answer", Title: "Answer a task",
		Description: "Answer a task that waits for input: one waiting on you, or one you sent that asks you something. " +
			"Use decision approve or reject for approvals."}, s.answer)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_runs", Title: "Search runs", Annotations: readOnly,
		Description: "Search runs (a task and everything it caused) by text, status (running, waiting, done, failed), participant, or who they wait on. Newest first."}, s.searchRuns)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_run", Title: "Get a run", Annotations: readOnly,
		Description: "Get a run with all its tasks: who called whom, each task's state and answer."}, s.getRun)
	return srv
}

// --- results ---

// TaskResult is a task's state and what it answered.
type TaskResult = client.Result

func terminal(s a2a.TaskState) bool { return s.Terminal() }

// --- tools ---

type agentSummary struct {
	Handle      string   `json:"handle"`
	Kind        string   `json:"kind" jsonschema:"agent or human"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Online      bool     `json:"online"`
	Skills      []string `json:"skills,omitempty"`
}

type listAgentsIn struct {
	Kind string `json:"kind,omitempty" jsonschema:"only agent or only human; empty lists both"`
}

type listAgentsOut struct {
	Addresses []agentSummary `json:"addresses"`
}

func (s *server) listAgents(ctx context.Context, _ *mcp.CallToolRequest, in listAgentsIn) (*mcp.CallToolResult, listAgentsOut, error) {
	all, err := s.c.Addresses(ctx)
	if err != nil {
		return nil, listAgentsOut{}, err
	}
	out := listAgentsOut{Addresses: []agentSummary{}}
	for _, a := range all {
		if in.Kind != "" && a.Kind != in.Kind {
			continue
		}
		sum := agentSummary{Handle: "@" + a.Handle, Kind: a.Kind, Name: a.DisplayName, Description: a.Description, Online: a.Online}
		var card struct {
			Description string `json:"description"`
			Skills      []struct {
				Name string `json:"name"`
			} `json:"skills"`
		}
		if json.Unmarshal(a.Card, &card) == nil {
			if sum.Description == "" {
				sum.Description = card.Description
			}
			for _, sk := range card.Skills {
				sum.Skills = append(sum.Skills, sk.Name)
			}
		}
		out.Addresses = append(out.Addresses, sum)
	}
	return nil, out, nil
}

type handleIn struct {
	Handle string `json:"handle" jsonschema:"the address, e.g. @acme-buyer"`
}

func handleOf(h string) string { return strings.TrimPrefix(strings.TrimSpace(h), "@") }

func (s *server) address(ctx context.Context, handle string) (*client.Address, error) {
	all, err := s.c.Addresses(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.Handle == handle {
			return a, nil
		}
	}
	return nil, fmt.Errorf("no address @%s in this workspace (list_agents shows who you can reach)", handle)
}

func (s *server) agentCard(ctx context.Context, _ *mcp.CallToolRequest, in handleIn) (*mcp.CallToolResult, any, error) {
	a, err := s.address(ctx, handleOf(in.Handle))
	if err != nil {
		return nil, nil, err
	}
	if len(a.Card) == 0 || string(a.Card) == "null" {
		return nil, nil, fmt.Errorf("@%s has no card yet (is it connected?)", a.Handle)
	}
	var card map[string]any
	if err := json.Unmarshal(a.Card, &card); err != nil {
		return nil, nil, err
	}
	return nil, card, nil
}

type sendTaskIn struct {
	To          string         `json:"to" jsonschema:"the address, e.g. @acme-buyer"`
	Text        string         `json:"text,omitempty" jsonschema:"the message"`
	Data        map[string]any `json:"data,omitempty" jsonschema:"structured content to send along with (or instead of) the text"`
	TaskID      string         `json:"taskId,omitempty" jsonschema:"continue this task, e.g. to answer what it asked"`
	WaitSeconds *int           `json:"waitSeconds,omitempty" jsonschema:"how long to wait for the task to finish or need input (default 60, max 600, 0 returns at once)"`
}

func waitFor(sec *int, def time.Duration) time.Duration {
	if sec == nil {
		return def
	}
	return min(time.Duration(max(*sec, 0))*time.Second, maxWait)
}

func (s *server) sendTask(ctx context.Context, _ *mcp.CallToolRequest, in sendTaskIn) (*mcp.CallToolResult, *TaskResult, error) {
	to := handleOf(in.To)
	if to == "" || (in.Text == "" && len(in.Data) == 0) {
		return nil, nil, errors.New("send_task needs to and text (or data)")
	}
	res, err := s.c.Send(ctx, &client.Send{To: to, Text: in.Text, Data: in.Data, TaskID: in.TaskID})
	if err != nil {
		return nil, nil, err
	}
	if res.Message != nil {
		return nil, client.MessageResult(res.Message), nil
	}
	r, err := s.follow(ctx, string(res.Task.ID), waitFor(in.WaitSeconds, defaultSendWait), func(st a2a.TaskState) bool { return terminal(st) || client.Waiting(st) })
	return nil, r, err
}

type askHumanIn struct {
	To          string         `json:"to" jsonschema:"the person's address, e.g. @ada"`
	Question    string         `json:"question" jsonschema:"what to ask; say what you need and why, they read it on their phone"`
	Data        map[string]any `json:"data,omitempty" jsonschema:"details to show with the question, e.g. an order"`
	WaitSeconds *int           `json:"waitSeconds,omitempty" jsonschema:"how long to wait for the answer (default 120, max 600)"`
}

func (s *server) askHuman(ctx context.Context, _ *mcp.CallToolRequest, in askHumanIn) (*mcp.CallToolResult, *TaskResult, error) {
	to := handleOf(in.To)
	if strings.TrimSpace(in.Question) == "" {
		return nil, nil, errors.New("ask_human needs a question")
	}
	a, err := s.address(ctx, to)
	if err != nil {
		return nil, nil, err
	}
	if a.Kind != "human" {
		return nil, nil, fmt.Errorf("@%s is an agent: use send_task for agents, ask_human for people", to)
	}
	res, err := s.c.Send(ctx, &client.Send{To: to, Text: in.Question, Data: in.Data})
	if err != nil {
		return nil, nil, err
	}
	if res.Message != nil {
		return nil, client.MessageResult(res.Message), nil
	}
	r, err := s.follow(ctx, string(res.Task.ID), waitFor(in.WaitSeconds, defaultAskWait), terminal)
	return nil, r, err
}

// follow polls a task until done says so or the wait runs out, and returns where it is.
func (s *server) follow(ctx context.Context, id string, wait time.Duration, done func(a2a.TaskState) bool) (*TaskResult, error) {
	t, ok, err := s.c.Follow(ctx, id, wait, s.poll, func(t *client.Task) bool { return done(t.Task.Status.State) })
	if err != nil {
		return nil, err
	}
	r := t.Result()
	if !ok {
		r.Note = "Still " + r.State + ". Call get_task with this taskId later; don't send it again."
	}
	return r, nil
}

type taskIn struct {
	TaskID string `json:"taskId"`
}

func (s *server) getTask(ctx context.Context, _ *mcp.CallToolRequest, in taskIn) (*mcp.CallToolResult, *TaskResult, error) {
	t, err := s.c.Task(ctx, in.TaskID)
	if err != nil {
		return nil, nil, err
	}
	return nil, t.Result(), nil
}

type waitSummary struct {
	TaskID      string `json:"taskId"`
	RunID       string `json:"runId"`
	Question    string `json:"question"`
	Asker       string `json:"asker"`
	WaitingOn   string `json:"waitingOn"`
	ForYou      bool   `json:"forYou"`
	Since       string `json:"since"`
	Reminders   int    `json:"reminders,omitempty"`
	EscalatedTo string `json:"escalatedTo,omitempty"`
}

type listWaitingOut struct {
	Waiting []waitSummary `json:"waiting"`
}

func (s *server) listWaiting(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listWaitingOut, error) {
	me, err := s.c.Me(ctx)
	if err != nil {
		return nil, listWaitingOut{}, err
	}
	ws, err := s.c.Waiting(ctx)
	if err != nil {
		return nil, listWaitingOut{}, err
	}
	out := listWaitingOut{Waiting: []waitSummary{}}
	for _, w := range ws {
		sum := waitSummary{RunID: w.RunID, Asker: "@" + w.Asker, WaitingOn: "@" + w.Who, Since: w.Since.Format(time.RFC3339),
			Reminders: w.Reminders, ForYou: w.Who == me.User.Handle || w.EscalatedTo == me.User.Handle}
		if w.EscalatedTo != "" {
			sum.EscalatedTo = "@" + w.EscalatedTo
		}
		if w.Task != nil {
			sum.TaskID, sum.Question = w.Task.ID, firstOf(w.Task.Status, w.Task.Summary)
		}
		out.Waiting = append(out.Waiting, sum)
	}
	return nil, out, nil
}

type answerIn struct {
	TaskID   string         `json:"taskId"`
	Text     string         `json:"text,omitempty" jsonschema:"the answer"`
	Data     map[string]any `json:"data,omitempty" jsonschema:"a structured answer, when the task asked for one"`
	Decision string         `json:"decision,omitempty" jsonschema:"approve or reject, for approvals"`
}

type answerOut struct {
	Answered bool   `json:"answered"`
	TaskID   string `json:"taskId"`
}

func (s *server) answer(ctx context.Context, _ *mcp.CallToolRequest, in answerIn) (*mcp.CallToolResult, answerOut, error) {
	if in.Decision != "" && in.Decision != "approve" && in.Decision != "reject" {
		return nil, answerOut{}, errors.New("decision is approve or reject")
	}
	if in.Text == "" && len(in.Data) == 0 && in.Decision == "" {
		return nil, answerOut{}, errors.New("answer needs text, data or a decision")
	}
	text := in.Text
	if text == "" && in.Decision != "" {
		text = map[string]string{"approve": "Approved.", "reject": "Rejected."}[in.Decision]
	}
	if err := s.c.Answer(ctx, in.TaskID, &client.Answer{Text: text, Data: in.Data, Decision: in.Decision}); err != nil {
		return nil, answerOut{}, err
	}
	return nil, answerOut{Answered: true, TaskID: in.TaskID}, nil
}

type searchRunsIn struct {
	Q           string `json:"q,omitempty" jsonschema:"text in the run's title or participants, e.g. PO-123"`
	Status      string `json:"status,omitempty" jsonschema:"running, waiting, done or failed"`
	Participant string `json:"participant,omitempty" jsonschema:"an address that took part"`
	WaitingOn   string `json:"waitingOn,omitempty" jsonschema:"an address the run waits on"`
	Limit       int    `json:"limit,omitempty" jsonschema:"at most this many (default 20)"`
}

type runSummary struct {
	RunID        string   `json:"runId"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	Outcome      string   `json:"outcome,omitempty"`
	Starter      string   `json:"starter"`
	Recipient    string   `json:"recipient"`
	WaitingOn    []string `json:"waitingOn,omitempty"`
	Participants []string `json:"participants,omitempty"`
	Tasks        int      `json:"tasks"`
	UpdatedAt    string   `json:"updatedAt"`
}

type searchRunsOut struct {
	Runs []runSummary `json:"runs"`
}

func at(hs []string) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = "@" + h
	}
	return out
}

func summarizeRun(r *client.Run) runSummary {
	return runSummary{RunID: r.ID, Title: r.Title, State: client.ShortState(r.State), Outcome: r.Outcome, Starter: "@" + r.Starter, Recipient: "@" + r.Recipient,
		WaitingOn: at(r.Waiting), Participants: at(r.Participants), Tasks: r.Tasks, UpdatedAt: r.UpdatedAt.Format(time.RFC3339)}
}

func (s *server) searchRuns(ctx context.Context, _ *mcp.CallToolRequest, in searchRunsIn) (*mcp.CallToolResult, searchRunsOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	runs, err := s.c.Runs(ctx, client.RunFilter{Q: in.Q, Status: in.Status, Participant: handleOf(in.Participant), WaitingOn: handleOf(in.WaitingOn), Limit: limit})
	if err != nil {
		return nil, searchRunsOut{}, err
	}
	out := searchRunsOut{Runs: []runSummary{}}
	for _, r := range runs {
		out.Runs = append(out.Runs, summarizeRun(r))
	}
	return nil, out, nil
}

type runIn struct {
	RunID string `json:"runId"`
}

type runTask struct {
	TaskID     string `json:"taskId"`
	Parent     string `json:"parentTaskId,omitempty"`
	From       string `json:"from"`
	To         string `json:"to"`
	State      string `json:"state"`
	Summary    string `json:"summary,omitempty"`
	Answer     string `json:"answer,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	WaitingOn  string `json:"waitingOn,omitempty"`
}

type getRunOut struct {
	Run   runSummary `json:"run"`
	Tasks []runTask  `json:"tasks"`
}

func (s *server) getRun(ctx context.Context, _ *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, getRunOut, error) {
	d, err := s.c.Run(ctx, in.RunID)
	if err != nil {
		return nil, getRunOut{}, err
	}
	if d.Run == nil {
		return nil, getRunOut{}, errors.New("no such run")
	}
	out := getRunOut{Run: summarizeRun(d.Run), Tasks: []runTask{}}
	for _, t := range d.Tasks {
		rt := runTask{TaskID: t.ID, Parent: t.ParentTaskID, From: "@" + t.Sender, To: "@" + t.Recipient, State: client.ShortState(t.State), Summary: t.Summary}
		if t.Task != nil {
			r := t.Result()
			rt.Answer, rt.AnsweredBy, rt.WaitingOn = r.Text, r.AnsweredBy, r.WaitingOn
		}
		out.Tasks = append(out.Tasks, rt)
	}
	return nil, out, nil
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
