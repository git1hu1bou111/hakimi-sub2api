package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

func jsonoptEqualOutput(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}
func jsonoptSchemaCompare(t *testing.T, body []byte) {
	t.Helper()
	saved := append([]byte(nil), body...)
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGrok, "unknown"} {
		old, oc, oe := oracleSanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
		got, gc, ge := sanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
		if oc != gc || fmt.Sprint(oe) != fmt.Sprint(ge) || !jsonoptEqualOutput(old, got) {
			t.Fatalf("platform=%s input=%q old=(%q,%v,%v) got=(%q,%v,%v)", platform, body, old, oc, oe, got, gc, ge)
		}
		if !bytes.Equal(saved, body) {
			t.Fatal("request mutated")
		}
	}
}
func TestJSONOptSchemaDifferential(t *testing.T) {
	seeds := [][]byte{
		nil, []byte(`not-json`), []byte(`{"input":[{"type":"message","content":"hello"}],"tools":[]}`),
		[]byte(`{"tools":[{"type":"function","name":"x","parameters":{"type":null,"required":null,"pattern":"(?=x)","properties":{"x":{"type":"string","pattern":"\\p{L}"}}}}]}`),
		[]byte(`{"input":[{"parameters":{"type":null,"pattern":"(?=x)"},"type":"function","type":"message"}]}`),
		[]byte(`{"input":[{"type":"message","type":"function","parameters":{"type":null}}]}`),
		[]byte(`{"tools":[{"parameters":{"required":null, "pattern":"(?=x)", "type":null}}]}`),
		[]byte(`{"tools":[{"parameters":{"patternProperties":{"(?=x)":{"type":null,"required":null}},"additionalProperties":false,"type":null}}]}`),
		[]byte(`{"tools":[{"parameters":{"type":null,"type":"string","default":{"type":null,"required":null,"pattern":"(?=x)"}}}]}`),
		[]byte(`{"tools":[{"parameters":{"oneOf":[{"type":"object"},{"type":"object"}],"required":null,"pattern":"(?=x)"}}]}`),
	}
	for _, b := range seeds {
		jsonoptSchemaCompare(t, b)
	}
	r := rand.New(rand.NewSource(20261125))
	for n := 0; n < 2000; n++ {
		patterns := []string{"abc", "(?=x)", `\p{L}`, `(?<name>x)`, "[a-z]", "x\\\"y", "汉字🙂"}
		schema := map[string]any{"type": []any{nil, "object", "string"}[r.Intn(3)], "pattern": patterns[r.Intn(len(patterns))], "required": []any{nil, []any{}, []any{"x"}}[r.Intn(3)]}
		schema["properties"] = map[string]any{"x": map[string]any{"type": "string", "pattern": patterns[r.Intn(len(patterns))], "default": map[string]any{"required": nil, "pattern": "(?=opaque)"}}}
		if n%4 == 0 {
			schema["patternProperties"] = map[string]any{patterns[r.Intn(len(patterns))]: map[string]any{"type": "string"}}
			schema["additionalProperties"] = false
		}
		tool := map[string]any{"type": "function", "name": "sample", "parameters": schema}
		message := map[string]any{"type": "message", "content": fmt.Sprintf(`opaque %d \"quoted\" (?=literal)`, n)}
		var payload any = map[string]any{"input": []any{message, tool}, "tools": []any{map[string]any{"type": "namespace", "name": "test", "tools": []any{tool}}}}
		if n%3 == 0 {
			payload = map[string]any{"response": payload}
		}
		if n%3 == 1 {
			payload = map[string]any{"session": payload}
		}
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if n%7 == 0 {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, b, "", " "); err != nil {
				t.Fatal(err)
			}
			b = pretty.Bytes()
		}
		jsonoptSchemaCompare(t, b)
		if n < 300 {
			for _, cut := range []int{0, 1, len(b) / 2, len(b) - 1} {
				jsonoptSchemaCompare(t, b[:cut])
			}
		}
	}
	// Limit and escaped-string handling retain the original parser's decisions.
	for _, depth := range []int{127, 128, 129, 160} {
		b := append([]byte(`{"input":[{"type":"message","x":`), bytes.Repeat([]byte("["), depth)...)
		b = append(b, []byte(`"escaped \\\" quote"`)...)
		b = append(b, bytes.Repeat([]byte("]"), depth)...)
		b = append(b, []byte(`}]}`)...)
		jsonoptSchemaCompare(t, b)
	}
	t.Log("2000 generated + malformed/duplicate/Unicode/depth fixtures; four platforms; original input unchanged")
}
func FuzzJSONOptSchema(f *testing.F) {
	f.Add([]byte(`{"tools":[{"parameters":{"type":null,"pattern":"(?=x)","required":null}}],"input":[{"type":"message","content":"hello"}]}`))
	f.Add([]byte(`{"input":[{"type":"function","parameters":{"oneOf":[{"type":"object"}]}}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			t.Skip()
		}
		jsonoptSchemaCompare(t, b)
	})
}
