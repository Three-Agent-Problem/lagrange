package code

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestFromGitAndArtifact(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Ada", "-c", "user.email=ada@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("a.txt", "one\ntwo\n")
	run("add", ".")
	run("commit", "-qm", "first")
	first, patch, err := FromGit(t.Context(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Parents) != 0 || first.Insertions != 2 || !strings.Contains(patch, "+two") {
		t.Fatalf("root commit = %+v\n%s", first, patch)
	}

	write("a.txt", "one\n2\n")
	write("b.bin", "\x00\x01")
	run("add", ".")
	run("commit", "-qm", "second\n\nwith a body")
	c, patch, err := FromGit(t.Context(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "second" || c.Body != "with a body" || c.Author != "Ada <ada@example.com>" || c.At.IsZero() ||
		len(c.Files) != 2 || c.Insertions != 1 || c.Deletions != 1 || c.Parents[0] != first.SHA {
		t.Fatalf("commit = %+v", c)
	}
	c.Branch = "main"

	a := Artifact(c, patch)
	if a.Name != ArtifactName || a.ID != a2a.ArtifactID("commit-"+c.SHA) || a.Description != c.Summary() {
		t.Fatalf("artifact = %+v", a)
	}
	got, gotPatch, ok := Find(a.Parts)
	if !ok || got.SHA != c.SHA || got.Branch != "main" || got.Files[0].Path != "a.txt" || gotPatch != patch {
		t.Fatalf("found %+v %v", got, ok)
	}
	if _, _, ok := Find(a2a.ContentParts{a2a.NewTextPart("hi")}); ok {
		t.Fatal("a commit in plain text")
	}

	big := Artifact(c, strings.Repeat("x", MaxPatch+10))
	if got, p, _ := Find(big.Parts); !got.PatchCut || len(p) != MaxPatch {
		t.Fatalf("cut: %v, %d", got.PatchCut, len(p))
	}
	if _, _, err := FromGit(t.Context(), dir, "nope"); err == nil {
		t.Fatal("no error for a bad revision")
	}
}
