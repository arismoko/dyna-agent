package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteInstallerVerifiesChecksumAndReplacesAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer is for Unix hosts")
	}
	root := t.TempDir()
	scriptBody, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "remote-install.sh")
	writeTestExecutable(t, script, scriptBody)

	fixture := filepath.Join(root, "release-dyna")
	releaseBody := []byte("#!/usr/bin/env bash\nprintf 'dyna release fixture\\n'\n")
	writeTestExecutable(t, fixture, releaseBody)
	sum := sha256.Sum256(releaseBody)
	asset := "dyna_" + runtime.GOOS + "_" + runtime.GOARCH
	checksums := filepath.Join(root, "checksums.txt")
	if err := os.WriteFile(checksums, []byte(hex.EncodeToString(sum[:])+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	uname := `#!/usr/bin/env bash
case "$1" in
  -s) printf '` + installerUnameOS(runtime.GOOS) + `\n' ;;
  -m) printf '` + installerUnameArch(runtime.GOARCH) + `\n' ;;
  *) exit 2 ;;
esac
`
	writeTestExecutable(t, filepath.Join(fakeBin, "uname"), []byte(uname))
	curl := `#!/usr/bin/env bash
set -euo pipefail
url=""
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */checksums.txt) cp "$FIXTURE_CHECKSUMS" "$out" ;;
  */dyna_*) cp "$FIXTURE_BINARY" "$out" ;;
  *) exit 22 ;;
esac
`
	writeTestExecutable(t, filepath.Join(fakeBin, "curl"), []byte(curl))

	installDir := filepath.Join(root, "bin")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "dyna")
	oldBody := []byte("old installed binary\n")
	writeTestExecutable(t, target, oldBody)
	oldHandle, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer oldHandle.Close()

	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"HOME="+root,
		"DYNA_INSTALL_DIR="+installDir,
		"DYNA_NO_SKILLS=1",
		"FIXTURE_BINARY="+fixture,
		"FIXTURE_CHECKSUMS="+checksums,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "verified sha256") {
		t.Fatalf("installer output did not confirm verification:\n%s", out)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(releaseBody) {
		t.Fatalf("installed body = %q, want %q", installed, releaseBody)
	}
	stillOld, err := io.ReadAll(oldHandle)
	if err != nil {
		t.Fatal(err)
	}
	if string(stillOld) != string(oldBody) {
		t.Fatalf("open old inode changed: got %q, want %q", stillOld, oldBody)
	}
}

func TestRemoteInstallerRejectsChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer is for Unix hosts")
	}
	// The updater package exercises the same mismatch invariant in detail; this
	// focused shell check proves install.sh fails before activating its stage.
	root := t.TempDir()
	scriptBody, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "remote-install.sh")
	writeTestExecutable(t, script, scriptBody)
	fixture := filepath.Join(root, "release-dyna")
	writeTestExecutable(t, fixture, []byte("#!/usr/bin/env bash\necho fixture\n"))
	checksums := filepath.Join(root, "checksums.txt")
	asset := "dyna_" + runtime.GOOS + "_" + runtime.GOARCH
	if err := os.WriteFile(checksums, []byte(strings.Repeat("0", 64)+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, filepath.Join(fakeBin, "uname"), []byte("#!/usr/bin/env bash\n[ \"$1\" = -s ] && echo "+installerUnameOS(runtime.GOOS)+" || echo "+installerUnameArch(runtime.GOARCH)+"\n"))
	writeTestExecutable(t, filepath.Join(fakeBin, "curl"), []byte("#!/usr/bin/env bash\nset -eu\nout=\"\"; url=\"\"\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=\"$2\"; shift 2;; -*) shift;; *) url=\"$1\"; shift;; esac; done\ncase \"$url\" in */checksums.txt) cp \"$FIXTURE_CHECKSUMS\" \"$out\";; *) cp \"$FIXTURE_BINARY\" \"$out\";; esac\n"))
	installDir := filepath.Join(root, "bin")
	if err := os.Mkdir(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "dyna")
	want := []byte("keep me\n")
	writeTestExecutable(t, target, want)
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"), "HOME="+root,
		"DYNA_INSTALL_DIR="+installDir, "DYNA_NO_SKILLS=1",
		"FIXTURE_BINARY="+fixture, "FIXTURE_CHECKSUMS="+checksums,
	)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "checksum mismatch") {
		t.Fatalf("installer error = %v, output:\n%s", err, out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(want) {
		t.Fatalf("checksum mismatch replaced target: got %q want %q", got, want)
	}
}

func writeTestExecutable(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
}

func installerUnameOS(goos string) string {
	switch goos {
	case "darwin":
		return "Darwin"
	default:
		return "Linux"
	}
}

func installerUnameArch(goarch string) string {
	if goarch == "arm64" {
		return "arm64"
	}
	return "x86_64"
}
