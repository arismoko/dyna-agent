package pi

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeSizedJournalRecord(t *testing.T, file *os.File, totalBytes int, prefix, suffix string) {
	t.Helper()
	padding := totalBytes - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("journal fixture record size %d is smaller than framing %d", totalBytes, len(prefix)+len(suffix))
	}
	if _, err := file.WriteString(prefix); err != nil {
		t.Fatal(err)
	}
	block := strings.Repeat("x", 64*1024)
	for padding > 0 {
		chunk := min(padding, len(block))
		if _, err := file.WriteString(block[:chunk]); err != nil {
			t.Fatal(err)
		}
		padding -= chunk
	}
	if _, err := file.WriteString(suffix); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
}

func readNULArgs(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\x00"), "\x00")
}

func anyStrings(t *testing.T, value any) []string {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want string array", value)
	}
	result := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("value[%d] = %#v, want string", i, value)
		}
		result[i] = text
	}
	return result
}

func containsArgs(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func piCommandSubprocess(t *testing.T, binDir string, args ...string) *exec.Cmd {
	t.Helper()
	rawArgs := ""
	if len(args) > 0 {
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		rawArgs = string(encoded)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPiCommandSubprocess$")
	cmd.Env = append(os.Environ(),
		"GO_PI_COMMAND_HELPER=1",
		"GO_PI_COMMAND_ARGS="+rawArgs,
		"PATH="+binDir,
		"HOME="+t.TempDir(),
		"XDG_DATA_HOME="+t.TempDir(),
		"DYNA_NO_AUTO_UPDATE=1",
	)
	return cmd
}
