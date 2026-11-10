package code

import (
	"context"
	"iter"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// Workspace is where an agent works on a task: a git checkout and its branch.
type Workspace struct {
	Dir    string
	Branch string
	// Start is a repository whose HEAD is where Dir starts, for a Dir that doesn't
	// exist before the context's first task (a worktree the agent creates).
	Start string
}

// Publish wraps an agent executor so that the commits it makes in a context's
// workspace are published as commit artifacts of the task that made them, just
// before the task ends or asks something. Each commit is published once.
// workspace says where a task's context works; an empty Dir skips the task.
func Publish(exec a2asrv.AgentExecutor, workspace func(*a2asrv.ExecutorContext) Workspace, logger *slog.Logger) a2asrv.AgentExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &publisher{AgentExecutor: exec, workspace: workspace, logger: logger, seen: map[string]string{}}
}

type publisher struct {
	a2asrv.AgentExecutor
	workspace func(*a2asrv.ExecutorContext) Workspace
	logger    *slog.Logger

	mu   sync.Mutex
	seen map[string]string // workspace dir -> the last commit published (or where it started)
}

func (p *publisher) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	ws := p.workspace(ec)
	if ws.Dir == "" {
		return p.AgentExecutor.Execute(ctx, ec)
	}
	return func(yield func(a2a.Event, error) bool) {
		p.mu.Lock()
		if p.seen[ws.Dir] == "" {
			from := ws.Dir
			if _, err := os.Stat(ws.Dir); err != nil && ws.Start != "" {
				from = ws.Start
			}
			p.seen[ws.Dir] = revParse(ctx, from, "HEAD")
		}
		p.mu.Unlock()
		for ev, err := range p.AgentExecutor.Execute(ctx, ec) {
			if st, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && err == nil && pauses(st.Status.State) {
				if !p.publish(ctx, ec, ws, yield) {
					return
				}
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}

func pauses(s a2a.TaskState) bool {
	return s.Terminal() || s == a2a.TaskStateInputRequired || s == a2a.TaskStateAuthRequired
}

// publish yields an artifact for each commit since the last one published.
func (p *publisher) publish(ctx context.Context, ec *a2asrv.ExecutorContext, ws Workspace, yield func(a2a.Event, error) bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	from := p.seen[ws.Dir]
	if from == "" {
		if from = revParse(ctx, ws.Dir, "HEAD"); from != "" {
			p.seen[ws.Dir] = from // nothing to compare with: publish from now on
		}
		return true
	}
	out, err := git(ctx, ws.Dir, "rev-list", "--reverse", from+"..HEAD")
	if err != nil {
		p.logger.Warn("code: listing the agent's commits", "dir", ws.Dir, "error", err)
		return true
	}
	for _, sha := range strings.Fields(out) {
		c, patch, err := FromGit(ctx, ws.Dir, sha)
		if err != nil {
			p.logger.Warn("code: reading a commit", "sha", sha, "error", err)
			continue
		}
		c.Branch = ws.Branch
		if !yield(Event(ec, c, patch), nil) {
			return false
		}
		p.seen[ws.Dir] = sha
	}
	return true
}

func revParse(ctx context.Context, dir, rev string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", rev).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
