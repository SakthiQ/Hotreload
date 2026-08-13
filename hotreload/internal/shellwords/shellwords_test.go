package shellwords

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t ", nil},
		{"single word", "./server", []string{"./server"}},
		{"several words", "go build -o server .", []string{"go", "build", "-o", "server", "."}},
		{"collapses runs of spaces", "a   b\tc", []string{"a", "b", "c"}},
		{
			"double-quoted argument with a space",
			`./server.exe --config "my dir/config.json"`,
			[]string{"./server.exe", "--config", "my dir/config.json"},
		},
		{
			"single-quoted argument with a space",
			`./server --flag 'two words'`,
			[]string{"./server", "--flag", "two words"},
		},
		{
			"quotes inside a word are stripped",
			`--name="hot reload"`,
			[]string{"--name=hot reload"},
		},
		{
			"backslashes are literal so Windows paths survive",
			`C:\Program\server.exe -v`,
			[]string{`C:\Program\server.exe`, "-v"},
		},
		{"empty quoted argument is kept", `run "" x`, []string{"run", "", "x"}},
		{"other quote type inside quotes is literal", `say "it's fine"`, []string{"say", "it's fine"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Split(tt.in)
			if err != nil {
				t.Fatalf("Split(%q) returned error: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitUnterminatedQuote(t *testing.T) {
	for _, in := range []string{`./server "arg`, `./server 'arg`} {
		if _, err := Split(in); err == nil {
			t.Errorf("Split(%q) succeeded, want an unterminated-quote error", in)
		}
	}
}
