package platform

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestDecisionData(t *testing.T) {
	approval := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"approved": {Type: "boolean"}}}
	form := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"approved": {Type: "boolean"}, "note": {Type: "string"}}}
	for _, tc := range []struct {
		schema   *jsonschema.Schema
		decision string
		want     map[string]any
	}{
		{approval, "approve", map[string]any{"approved": true}},
		{approval, "reject", map[string]any{"approved": false}},
		{approval, "", nil},
		{form, "approve", nil},
		{&jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"size": {Type: "string"}}}, "approve", nil},
		{nil, "approve", nil},
	} {
		if got := decisionData(tc.schema, tc.decision); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("decisionData(%v, %q) = %v, want %v", tc.schema, tc.decision, got, tc.want)
		}
	}
}
