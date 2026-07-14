package engine

import "testing"

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{
			name:    "bare object with markdown fences inside a string",
			in:      "{\"report\": \"# Title\\n\\n```go\\nfunc main() {}\\n```\\nmore prose\"}",
			wantKey: "report", wantVal: "# Title\n\n```go\nfunc main() {}\n```\nmore prose",
		},
		{
			name:    "fenced json",
			in:      "```json\n{\"a\": \"b\"}\n```",
			wantKey: "a", wantVal: "b",
		},
		{
			name:    "prose then fenced json",
			in:      "Here is the result:\n```json\n{\"a\": \"b\"}\n```\nDone.",
			wantKey: "a", wantVal: "b",
		},
		{
			name:    "prose with trailing bare object",
			in:      "The answer follows. {\"a\": \"b\"}",
			wantKey: "a", wantVal: "b",
		},
		{name: "no json at all", in: "just words, no data", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := extractJSON(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractJSON: %v", err)
			}
			obj, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("expected object, got %T", v)
			}
			if got := obj[tc.wantKey]; got != tc.wantVal {
				t.Fatalf("key %q = %#v, want %q", tc.wantKey, got, tc.wantVal)
			}
		})
	}
}
