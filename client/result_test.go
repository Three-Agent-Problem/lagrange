package client

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestResultJoinsStreamedChunks(t *testing.T) {
	task := &a2a.Task{
		ID:     "t1",
		Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Artifacts: []*a2a.Artifact{
			{Parts: []*a2a.Part{a2a.NewTextPart("I fixed `add"), a2a.NewTextPart(".py` by chang"), a2a.NewTextPart("ing it.")}},
			{Parts: []*a2a.Part{a2a.NewTextPart("Second artifact."), a2a.NewDataPart(map[string]any{"n": 1})}},
		},
	}
	r := ResultOf(task)
	if want := "I fixed `add.py` by changing it.\nSecond artifact."; r.Text != want {
		t.Errorf("text = %q, want %q", r.Text, want)
	}
	if len(r.Data) != 1 {
		t.Errorf("data = %v", r.Data)
	}
}
