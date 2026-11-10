// Package code is Lagrange's convention for code changes: an agent that commits
// publishes each commit as an artifact of its task, so every Lagrange client can
// list a run's commits and show their diffs, not only the machine with the
// repository.
//
// The artifact is named "commit" (ID "commit-<sha>") and has a data part
// {"commit": {...}} (see [Commit]) and, optionally, the patch as a text part with
// the media type text/x-diff. The relay records the commit (without the patch)
// in the activity log as data.commit of the artifact entry; the patch stays in
// the task's artifact, for GET /api/tasks/{id}.
package code

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const (
	// ArtifactName is the name of a commit artifact.
	ArtifactName = "commit"
	// Key is the data part key of a commit.
	Key = "commit"
	// PatchMediaType is the media type of the patch part.
	PatchMediaType = "text/x-diff"
	// MaxPatch caps the patch in an artifact, in bytes. A longer patch is cut, and
	// Commit.PatchCut says so.
	MaxPatch = 256 << 10
)

// Commit describes a commit.
type Commit struct {
	SHA     string    `json:"sha"`
	Parents []string  `json:"parents,omitempty"`
	Branch  string    `json:"branch,omitempty"`
	Subject string    `json:"subject"`
	Body    string    `json:"body,omitempty"`
	Author  string    `json:"author,omitempty"`
	At      time.Time `json:"at"`
	// Files changed, with their line counts (zero for binary files).
	Files      []File `json:"files,omitempty"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	// PatchCut is set when the artifact's patch was cut to MaxPatch.
	PatchCut bool `json:"patchCut,omitempty"`
}

// File is a file a commit changed.
type File struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// Short returns the commit's SHA cut to 7 characters.
func (c Commit) Short() string {
	if len(c.SHA) > 7 {
		return c.SHA[:7]
	}
	return c.SHA
}

// Summary says the commit in a line: "3f2a1c9 add the store (+44 −9)".
func (c Commit) Summary() string {
	return fmt.Sprintf("%s %s (+%d −%d)", c.Short(), c.Subject, c.Insertions, c.Deletions)
}

// Map returns c as the JSON object of the commit data part.
func (c Commit) Map() map[string]any {
	raw, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// Artifact returns the commit's artifact, with patch if it isn't empty.
func Artifact(c Commit, patch string) *a2a.Artifact {
	if len(patch) > MaxPatch {
		patch, c.PatchCut = patch[:MaxPatch], true
	}
	parts := a2a.ContentParts{a2a.NewDataPart(map[string]any{Key: c.Map()})}
	if patch != "" {
		p := a2a.NewTextPart(patch)
		p.MediaType = PatchMediaType
		parts = append(parts, p)
	}
	return &a2a.Artifact{ID: a2a.ArtifactID("commit-" + c.SHA), Name: ArtifactName, Description: c.Summary(), Parts: parts}
}

// Event returns the artifact event that publishes a commit in a task.
func Event(info a2a.TaskInfoProvider, c Commit, patch string) *a2a.TaskArtifactUpdateEvent {
	ti := info.TaskInfo()
	return &a2a.TaskArtifactUpdateEvent{TaskID: ti.TaskID, ContextID: ti.ContextID, Artifact: Artifact(c, patch), LastChunk: true}
}

// Find returns the commit and the patch in parts, if they hold one.
func Find(parts []*a2a.Part) (Commit, string, bool) {
	var c Commit
	var patch string
	found := false
	for _, p := range parts {
		if d, ok := p.Data().(map[string]any); ok {
			if v, ok := d[Key]; ok {
				raw, err := json.Marshal(v)
				if err == nil && json.Unmarshal(raw, &c) == nil && c.SHA != "" {
					found = true
				}
			}
		}
		if p.MediaType == PatchMediaType && p.Text() != "" {
			patch = p.Text()
		}
	}
	return c, patch, found
}

// FromGit reads a commit (any revision git understands) and its patch from the
// repository in dir.
func FromGit(ctx context.Context, dir, rev string) (Commit, string, error) {
	const sep = "\x1f"
	out, err := git(ctx, dir, "show", "-s", "--format=%H"+sep+"%P"+sep+"%an <%ae>"+sep+"%aI"+sep+"%s"+sep+"%b", rev)
	if err != nil {
		return Commit{}, "", err
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), sep, 6)
	if len(f) < 6 {
		return Commit{}, "", fmt.Errorf("code: unexpected git show output for %s", rev)
	}
	c := Commit{SHA: f[0], Parents: strings.Fields(f[1]), Author: f[2], Subject: f[4], Body: strings.TrimSpace(f[5])}
	c.At, _ = time.Parse(time.RFC3339, f[3])
	// Against the first parent, so a merge shows what it brought in.
	base := c.SHA + "^"
	if len(c.Parents) == 0 {
		base = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // the empty tree
	}
	stat, err := git(ctx, dir, "diff", "--numstat", base, c.SHA)
	if err != nil {
		return Commit{}, "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(stat), "\n") {
		cols := strings.SplitN(line, "\t", 3)
		if len(cols) != 3 {
			continue
		}
		ins, _ := strconv.Atoi(cols[0]) // "-" for binary files
		del, _ := strconv.Atoi(cols[1])
		c.Files = append(c.Files, File{Path: cols[2], Insertions: ins, Deletions: del})
		c.Insertions += ins
		c.Deletions += del
	}
	patch, err := git(ctx, dir, "diff", base, c.SHA)
	if err != nil {
		return Commit{}, "", err
	}
	return c, patch, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
