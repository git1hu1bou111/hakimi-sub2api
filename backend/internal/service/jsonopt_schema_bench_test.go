package service

import (
	"fmt"
	"strings"
	"testing"
)

func jsonoptSchemaBody(history int) []byte {
	items := make([]string, history)
	for i := range items {
		items[i] = `{"type":"message","role":"user","content":"` + strings.Repeat("plain history ", 315) + `"}`
	}
	return []byte(`{"input":[` + strings.Join(items, ",") + `],"tools":[{"type":"function","name":"sample","parameters":{"type":null,"required":null,"properties":{"x":{"type":"string","pattern":"(?=x)"}}}}]}`)
}
func BenchmarkJSONOptSchema(b *testing.B) {
	for _, n := range []int{16, 256, 512} {
		body := jsonoptSchemaBody(n)
		b.Run(fmt.Sprintf("history_%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, err := sanitizeOpenAIResponsesToolSchemasForPlatform(body, PlatformOpenAI)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Compare against the frozen original parser using the identical synthetic body.
func BenchmarkJSONOptSchemaBaseline(b *testing.B) {
	for _, n := range []int{16, 256, 512} {
		body := jsonoptSchemaBody(n)
		b.Run(fmt.Sprintf("history_%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, err := oracleSanitizeOpenAIResponsesToolSchemasForPlatform(body, PlatformOpenAI)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
