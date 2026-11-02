package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/client"
)

// Where the API commands write; tests swap them.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	now              = time.Now
)

// Exit codes of send --wait and task --wait, so scripts can tell outcomes apart.
const (
	exitEnded   = 3 // the task failed, was rejected or canceled
	exitTimeout = 4 // --timeout ran out first; the task goes on
	exitAsksYou = 5 // the task waits on you: answer it with lagrange answer
)

// exitError ends the program with a code; msg (if any) goes to stderr.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// parseAll parses flags that may come before, between or after the positional
// arguments ("send @x hi --wait"), and returns the positional ones.
func parseAll(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		if args[0] == "--" {
			return append(pos, args[1:]...)
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// apiClient builds a client from the flags, the environment or the saved login.
func apiClient(af apiFlags) (*client.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return af.client(cfg)
}

// dataFlag reads --data: inline JSON, or @file.
func dataFlag(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	raw := []byte(s)
	if name, ok := strings.CutPrefix(s, "@"); ok {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("--data must be a JSON object: %w", err)
	}
	return m, nil
}

func handleArg(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "@") }

func printJSON(v any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ago is a short relative time: "just now", "5m ago", "3h ago", "2d ago".
func ago(t time.Time) string {
	d := now().Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func at(h string) string {
	if h == "" {
		return ""
	}
	return "@" + h
}

const sendUsage = `Usage: lagrange send @address "message" [flags]

Sends a message to an agent or a person and prints the task ID. With --wait it waits
for the task to end and prints the answer instead. A task that waits on someone else
(a question to @cfo, an approval) is waited through; one that asks you stops the wait.

Exit codes with --wait: 0 completed, 3 failed, rejected or canceled, 4 timed out,
5 the task asks you (answer it with lagrange answer).

Flags:
`

func send(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	af := addAPIFlags(fs)
	wait := fs.Bool("wait", false, "Wait for the task to end and print the answer.")
	timeout := fs.Duration("timeout", 0, "With --wait, give up after this long (exit 4); 0 waits as long as it takes.")
	data := fs.String("data", "", "Structured content to send: a JSON object, or @file.json.")
	taskID := fs.String("task", "", "Continue this task, e.g. to answer what an agent asked.")
	asJSON := fs.Bool("json", false, "Print the result as JSON.")
	fs.Usage = func() { fmt.Fprint(fs.Output(), sendUsage); fs.PrintDefaults() }
	pos := parseAll(fs, args)
	if len(pos) < 1 || (len(pos) < 2 && *data == "") {
		fs.Usage()
		return &exitError{code: 2}
	}
	d, err := dataFlag(*data)
	if err != nil {
		return err
	}
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	to := handleArg(pos[0])
	res, err := c.Send(ctx, &client.Send{To: to, Text: strings.Join(pos[1:], " "), Data: d, TaskID: *taskID})
	if err != nil {
		return err
	}
	if res.Message != nil {
		return printResult(client.MessageResult(res.Message), *asJSON)
	}
	id := string(res.Task.ID)
	if !*wait {
		if *asJSON {
			return printJSON(client.ResultOf(res.Task))
		}
		fmt.Fprintln(stdout, id)
		fmt.Fprintf(stderr, "Sent to @%s. Check on it with: lagrange task %s --wait\n", to, id)
		return nil
	}
	return waitTask(ctx, c, id, *timeout, *asJSON)
}

func task(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	af := addAPIFlags(fs)
	wait := fs.Bool("wait", false, "Wait for the task to end (exit codes as for send --wait).")
	timeout := fs.Duration("timeout", 0, "With --wait, give up after this long (exit 4).")
	asJSON := fs.Bool("json", false, "Print the result as JSON.")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: lagrange task <id> [flags]\n\nShows a task's state and answer.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	pos := parseAll(fs, args)
	if len(pos) != 1 {
		fs.Usage()
		return &exitError{code: 2}
	}
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	if *wait {
		return waitTask(ctx, c, pos[0], *timeout, *asJSON)
	}
	t, err := c.Task(ctx, pos[0])
	if err != nil {
		return err
	}
	return printResult(t.Result(), *asJSON)
}

// waitTask follows a task until it ends or waits on the caller, prints the result and
// returns the exit code that says how it went.
func waitTask(ctx context.Context, c *client.Client, id string, timeout time.Duration, asJSON bool) error {
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 100 * 365 * 24 * time.Hour
	}
	last := ""
	t, ok, err := c.Follow(ctx, id, timeout, pollEvery, func(t *client.Task) bool {
		st := t.Task.Status.State
		if who := t.WaitsOn(); who != "" && who != last && who != me.User.Handle {
			fmt.Fprintf(stderr, "Waiting on @%s…\n", who)
		}
		last = t.WaitsOn()
		return st.Terminal() || (client.Waiting(st) && last == me.User.Handle)
	})
	if err != nil {
		return err
	}
	r := t.Result()
	if err := printResult(r, asJSON); err != nil {
		return err
	}
	switch {
	case !ok:
		return &exitError{code: exitTimeout, msg: fmt.Sprintf("still %s; the task goes on. Wait again with: lagrange task %s --wait", r.State, id)}
	case client.Waiting(t.Task.Status.State):
		return &exitError{code: exitAsksYou, msg: fmt.Sprintf("the task asks you. Answer with: lagrange answer %s \"…\"", id)}
	case t.Task.Status.State != a2a.TaskStateCompleted:
		how := map[string]string{"rejected": "was rejected", "canceled": "was canceled"}[r.State]
		return &exitError{code: exitEnded, msg: "the task " + firstOf(how, r.State)}
	}
	return nil
}

// pollEvery is how often --wait checks a task.
var pollEvery = time.Second

// printResult prints the answer on stdout and how it went on stderr, or the whole result
// as JSON.
func printResult(r *client.Result, asJSON bool) error {
	if asJSON {
		return printJSON(r)
	}
	if r.Text != "" {
		fmt.Fprintln(stdout, r.Text)
	}
	for _, d := range r.Data {
		b, _ := json.Marshal(d)
		fmt.Fprintln(stdout, string(b))
	}
	line := r.State
	if r.AnsweredBy != "" {
		line += ", answered by @" + r.AnsweredBy
	}
	if r.WaitingOn != "" {
		line += ", waiting on @" + r.WaitingOn
	}
	if r.TaskID != "" {
		line += " (task " + r.TaskID + ")"
	}
	fmt.Fprintln(stderr, line)
	return nil
}

func answer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	af := addAPIFlags(fs)
	approve := fs.Bool("approve", false, "Approve.")
	reject := fs.Bool("reject", false, "Reject.")
	data := fs.String("data", "", "A structured answer: a JSON object, or @file.json.")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: lagrange answer <task-id> [\"answer\"] [--approve|--reject] [--data JSON]\n\nAnswers a task that waits on you.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	pos := parseAll(fs, args)
	if len(pos) < 1 || (*approve && *reject) {
		fs.Usage()
		return &exitError{code: 2}
	}
	d, err := dataFlag(*data)
	if err != nil {
		return err
	}
	a := &client.Answer{Text: strings.Join(pos[1:], " "), Data: d}
	switch {
	case *approve:
		a.Decision = "approve"
		a.Text = firstOf(a.Text, "Approved.")
	case *reject:
		a.Decision = "reject"
		a.Text = firstOf(a.Text, "Rejected.")
	}
	if a.Text == "" && a.Data == nil {
		return errors.New("say something: an answer, --approve, --reject or --data")
	}
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	if err := c.Answer(ctx, pos[0], a); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Answered %s.\n", pos[0])
	return nil
}

func waiting(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("waiting", flag.ExitOnError)
	af := addAPIFlags(fs)
	mine := fs.Bool("mine", false, "Only what waits on you.")
	asJSON := fs.Bool("json", false, "Print JSON.")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: lagrange waiting [flags]\n\nLists the tasks in the workspace that wait on someone.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	parseAll(fs, args)
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	all, err := c.Waiting(ctx)
	if err != nil {
		return err
	}
	ws := all[:0]
	for _, w := range all {
		if !*mine || w.Who == me.User.Handle || w.EscalatedTo == me.User.Handle {
			ws = append(ws, w)
		}
	}
	if *asJSON {
		return printJSON(map[string]any{"waiting": ws})
	}
	if len(ws) == 0 {
		fmt.Fprintln(stderr, "Nothing is waiting.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tWAITING ON\tASKED BY\tSINCE\tQUESTION")
	for _, w := range ws {
		who := at(w.Who)
		if w.Who == me.User.Handle {
			who += " (you)"
		}
		if w.EscalatedTo != "" {
			who += ", escalated to " + at(w.EscalatedTo)
		}
		id, q := "", ""
		if w.Task != nil {
			id, q = w.Task.ID, firstOf(w.Task.Status, w.Task.Summary)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, who, at(w.Asker), ago(w.Since), clip(q, 60))
	}
	return tw.Flush()
}

func runs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	af := addAPIFlags(fs)
	var f client.RunFilter
	fs.StringVar(&f.Q, "q", "", "Search titles and participants, e.g. PO-123.")
	fs.StringVar(&f.Status, "status", "", "running, waiting, done or failed.")
	fs.StringVar(&f.WaitingOn, "waiting-on", "", "Only runs waiting on this address.")
	fs.IntVar(&f.Limit, "limit", 20, "How many runs.")
	asJSON := fs.Bool("json", false, "Print JSON.")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: lagrange runs [flags]\n\nLists runs (a task and everything it caused), newest first.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	parseAll(fs, args)
	f.WaitingOn = handleArg(f.WaitingOn)
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	rs, err := c.Runs(ctx, f)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(map[string]any{"runs": rs})
	}
	if len(rs) == 0 {
		fmt.Fprintln(stderr, "No runs.")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tSTATE\tFROM → TO\tUPDATED\tTITLE")
	for _, r := range rs {
		fmt.Fprintf(tw, "%s\t%s\t%s → %s\t%s\t%s\n", r.ID, runState(r), at(r.Starter), at(r.Recipient), ago(r.UpdatedAt), clip(r.Title, 60))
	}
	return tw.Flush()
}

func runState(r *client.Run) string {
	s := client.ShortState(r.State)
	if len(r.Waiting) > 0 {
		var hs []string
		for _, h := range r.Waiting {
			hs = append(hs, at(h))
		}
		s += " (" + strings.Join(hs, ", ") + ")"
	}
	return s
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	af := addAPIFlags(fs)
	asJSON := fs.Bool("json", false, "Print JSON.")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: lagrange run <id> [flags]\n\nShows a run and its tasks.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	pos := parseAll(fs, args)
	if len(pos) != 1 {
		fs.Usage()
		return &exitError{code: 2}
	}
	c, err := apiClient(af)
	if err != nil {
		return err
	}
	d, err := c.Run(ctx, pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(d)
	}
	r := d.Run
	fmt.Fprintf(stdout, "%s\n%s · %s → %s · %s · started %s\n", r.Title, runState(r), at(r.Starter), at(r.Recipient), plural(r.Tasks, "task"), ago(r.StartedAt))
	if r.Outcome != "" {
		fmt.Fprintf(stdout, "Outcome: %s\n", clip(r.Outcome, 200))
	}
	fmt.Fprintln(stdout)
	depth := map[string]int{}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTATE\tFROM → TO\tSUMMARY")
	for _, t := range d.Tasks {
		depth[t.ID] = 0
		if p, ok := depth[t.ParentTaskID]; ok && t.ParentTaskID != "" {
			depth[t.ID] = p + 1
		}
		state := client.ShortState(t.State)
		if res := t.Result(); res.AnsweredBy != "" {
			state += " by " + at(res.AnsweredBy)
		} else if res.WaitingOn != "" {
			state += " on " + at(res.WaitingOn)
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s → %s\t%s\n", strings.Repeat("  ", depth[t.ID]), t.ID, state, at(t.Sender), at(t.Recipient), clip(t.Summary, 60))
	}
	return tw.Flush()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
