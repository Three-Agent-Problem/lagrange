package console

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/three-agent-problem/lagrange/credentials"
)

// AnsweredByKey is the message metadata key that names the person who answered from
// the inbox. Agents can copy it into their audit trail.
const AnsweredByKey = "https://threeagents.dev/ext/answered-by/v1"

// inboxItem is one task waiting on a person.
type inboxItem struct {
	Task      *Task
	Request   *structuredinput.Request
	Fields    []field
	Challenge *credentials.Challenge
}

// field is one form input generated from a JSON Schema property.
type field struct {
	Name     string
	Title    string
	Kind     string // "decision" (a lone boolean, shown as two buttons), "checkbox", "select", "number", "text"
	Options  []string
	Required bool
}

func (c *Console) inboxItems() []inboxItem {
	var items []inboxItem
	for _, t := range c.Tasks() {
		if !t.Waiting() {
			continue
		}
		item := inboxItem{Task: t}
		if t.State == a2a.TaskStateInputRequired {
			if req, err := structuredinput.ParseRequest(t.Status); err == nil && req != nil {
				item.Request = req
				item.Fields = formFields(req.Schema)
			}
		}
		if ch, ok := credentials.ChallengeFromMessage(t.Status); ok {
			item.Challenge = ch
		}
		items = append(items, item)
	}
	return items
}

// formFields turns an object schema into form inputs. Only flat objects with primitive
// properties are supported, which covers approval-style requests.
func formFields(schema *jsonschema.Schema) []field {
	if schema == nil {
		return nil
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range schema.Properties {
			if !yield(name) {
				return
			}
		}
	})
	booleans := 0
	for _, name := range names {
		if schemaType(schema.Properties[name]) == "boolean" {
			booleans++
		}
	}
	fields := make([]field, 0, len(names))
	for _, name := range names {
		prop := schema.Properties[name]
		f := field{Name: name, Title: firstNonEmpty(prop.Title, name), Required: slices.Contains(schema.Required, name)}
		switch t := schemaType(prop); {
		case t == "boolean" && booleans == 1:
			f.Kind = "decision"
		case t == "boolean":
			f.Kind = "checkbox"
		case len(prop.Enum) > 0:
			f.Kind = "select"
			for _, v := range prop.Enum {
				f.Options = append(f.Options, fmt.Sprint(v))
			}
		case t == "number" || t == "integer":
			f.Kind = "number"
		default:
			f.Kind = "text"
		}
		fields = append(fields, f)
	}
	// Decision buttons submit the form, so they go last.
	slices.SortStableFunc(fields, func(a, b field) int {
		if (a.Kind == "decision") == (b.Kind == "decision") {
			return 0
		}
		if a.Kind == "decision" {
			return 1
		}
		return -1
	})
	return fields
}

func schemaType(s *jsonschema.Schema) string {
	if s == nil {
		return ""
	}
	if s.Type != "" {
		return s.Type
	}
	for _, t := range s.Types {
		if t != "null" {
			return t
		}
	}
	return ""
}

// formData converts submitted form values into JSON values that match the schema.
func formData(schema *jsonschema.Schema, form func(string) string) (map[string]any, error) {
	data := make(map[string]any)
	for name, prop := range schema.Properties {
		v := form(name)
		switch schemaType(prop) {
		case "boolean":
			data[name] = v == "true" || v == "on"
		case "integer":
			if v == "" {
				continue
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be a whole number", name)
			}
			data[name] = n
		case "number":
			if v == "" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be a number", name)
			}
			data[name] = n
		default:
			if v != "" || slices.Contains(schema.Required, name) {
				data[name] = v
			}
		}
	}
	return data, nil
}

// Answer sends a person's answer to a task waiting in INPUT_REQUIRED, as a
// structuredinput response. The call returns as soon as the agent accepts the message;
// the task then continues on its own.
func (c *Console) Answer(ctx context.Context, taskID a2a.TaskID, answeredBy string, form func(string) string) error {
	t, ok := c.Task(taskID)
	if !ok {
		return fmt.Errorf("unknown task %s", taskID)
	}
	if t.State != a2a.TaskStateInputRequired {
		return fmt.Errorf("task %s is %s, not waiting for input", taskID, stateLabel(t.State))
	}
	req, err := structuredinput.ParseRequest(t.Status)
	if err != nil || req == nil {
		return fmt.Errorf("task %s did not ask for structured input", taskID)
	}
	data, err := formData(req.Schema, form)
	if err != nil {
		return err
	}
	msg, err := structuredinput.NewResponse(t, req, data)
	if err != nil {
		return err
	}
	if answeredBy != "" {
		msg.SetMeta(AnsweredByKey, answeredBy)
	}

	a, err := c.agent(ctx, t.AgentURL)
	if err != nil {
		return fmt.Errorf("reach %s: %w", t.AgentURL, err)
	}
	// The answer is logged before it is sent: the agent may resume and push its next
	// events before SendMessage returns.
	c.logEvent(taskID, "inbox", fmt.Sprintf("answered by %s: %s", firstNonEmpty(answeredBy, "someone"), compactJSON(data)))
	if _, err := a.client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true},
	}); err != nil {
		c.logEvent(taskID, "inbox", "the agent did not accept the answer: "+err.Error())
		return fmt.Errorf("send answer: %w", err)
	}
	return nil
}

func (c *Console) logEvent(taskID a2a.TaskID, kind, summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tasks[taskID]; ok {
		t.addEvent(time.Now(), kind, "", summary)
	}
}

func (c *Console) handleInbox(w http.ResponseWriter, r *http.Request) {
	c.render(w, "inbox.html", map[string]any{"Items": c.inboxItems(), "Error": r.URL.Query().Get("error")})
}

func (c *Console) handleAnswer(w http.ResponseWriter, r *http.Request) {
	id := a2a.TaskID(r.PathValue("id"))
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := c.Answer(ctx, id, r.Form.Get("_answered_by"), r.Form.Get); err != nil {
		http.Redirect(w, r, "/inbox?error="+urlQueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/tasks/"+string(id), http.StatusSeeOther)
}
