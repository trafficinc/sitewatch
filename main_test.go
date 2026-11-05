package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func put(t *testing.T, root, path, text string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, root, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
}

func statuses(t *testing.T, root string) map[string]string {
	t.Helper()
	_, pending, err := pendingChanges(root)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, item := range pending {
		result[item.Path] = item.Status
	}
	return result
}

func TestContentLifecycleAndIgnoreChanges(t *testing.T) {
	root := t.TempDir()
	gitignore := "/local/\n/secrets.php" // Preserve files lacking a final newline.
	put(t, root, ".gitignore", gitignore)
	put(t, root, "index.php", "alpha")
	put(t, root, "remove.txt", "delete after upload")
	put(t, root, "rename.txt", "renamed content")
	put(t, root, "local/notes.txt", "local")
	put(t, root, "secrets.php", "private")
	put(t, root, "tools/sitewatch/main.go", "tool source")
	put(t, root, "sitewatch", "binary")
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, err := readBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Files) != 3 {
		t.Fatalf("baseline contains local-only files: %#v", state.Files)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.HasPrefix(string(data), gitignore+"\n") {
		t.Fatal("existing gitignore was not preserved")
	}
	for _, rule := range []string{"/sitewatch\n", "/sitewatch.exe\n", "/.sitewatch/\n"} {
		if strings.Count(string(data), rule) != 1 {
			t.Fatalf("missing or repeated gitignore entry: %s", rule)
		}
	}
	info, err := os.Stat(filepath.Join(root, "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, "index.php", "bravo") // Same byte length and restored timestamp.
	if err := os.Chtimes(filepath.Join(root, "index.php"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	put(t, root, "assets/new file.txt", "new")
	put(t, root, "local/notes.txt", "changed locally")
	put(t, root, "secrets.php", "still private")
	remove(t, root, "remove.txt")
	if err := os.Rename(filepath.Join(root, "rename.txt"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"index.php": "MODIFIED", "assets/new file.txt": "ADDED", "remove.txt": "DELETED", "rename.txt": "DELETED", "renamed.txt": "ADDED"}
	if got := statuses(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("changes: %#v, want %#v", got, want)
	}
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	afterInit, _ := readBaseline(root)
	if afterInit.ID != state.ID || !reflect.DeepEqual(statuses(t, root), want) {
		t.Fatal("repeated init cleared pending uploads")
	}
	put(t, root, "index.php", "alpha")
	remove(t, root, "assets/new file.txt")
	delete(want, "index.php")
	delete(want, "assets/new file.txt")
	if got := statuses(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("reverted/new-then-deleted files should no longer be pending: %#v", got)
	}
	ignore, _ := os.ReadFile(filepath.Join(root, ignoreFilename))
	put(t, root, ignoreFilename, string(ignore)+"\n/remove.txt\n/renamed.txt\n")
	delete(want, "remove.txt")
	delete(want, "renamed.txt")
	if got := statuses(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("new exclusions should not produce false deletions: %#v", got)
	}
	if err := clearBaseline(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := statuses(t, root); len(got) != 0 {
		t.Fatalf("clear did not accept current files: %#v", got)
	}
	put(t, root, "index.php", "next upload")
	if got := statuses(t, root); got["index.php"] != "MODIFIED" {
		t.Fatal("next deployment cycle was not detected")
	}
	if found, err := findProject(filepath.Join(root, "assets")); err != nil || found != root {
		t.Fatalf("subdirectory root discovery: %s, %v", found, err)
	}
}

func TestIgnoreRulesAndExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	ignore := `/private/
*.log
/assets/**/*.map
.env*
!.env.example
/cache/
!/cache/
/cache/*
!/cache/keep.txt
draft[0-9].txt
temp?.txt
\#notes
`
	put(t, root, ignoreFilename, ignore)
	put(t, root, "index.php", "site")
	put(t, root, ".env", "private")
	put(t, root, ".env.example", "template")
	put(t, root, "nested/private/readme.txt", "public: root-anchored rule")
	put(t, root, "private/readme.txt", "private")
	put(t, root, "nested/error.log", "log")
	put(t, root, "assets/app.map", "map")
	put(t, root, "assets/deep/app.map", "map")
	put(t, root, "cache/keep.txt", "keep")
	put(t, root, "cache/ignore.txt", "ignore")
	put(t, root, "draft7.txt", "draft")
	put(t, root, "tempA.txt", "temp")
	put(t, root, "#notes", "notes")
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ignoreFilename))
	if string(data) != ignore {
		t.Fatal("existing ignore configuration was changed")
	}
	state, _ := readBaseline(root)
	want := map[string]string{"index.php": "", ".env.example": "", "nested/private/readme.txt": "", "cache/keep.txt": "", ".gitignore": ""}
	if len(state.Files) != len(want) {
		t.Fatalf("unexpected tracked paths: %#v", state.Files)
	}
	for path := range want {
		if _, exists := state.Files[path]; !exists {
			t.Fatalf("expected %s in baseline", path)
		}
	}
}

func TestFailedClearAndCorruptStatePreserveBaseline(t *testing.T) {
	root := t.TempDir()
	put(t, root, "index.php", "before")
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, stateDirectory, "state.json")
	original, _ := os.ReadFile(statePath)
	put(t, root, "index.php", "pending")
	put(t, root, ignoreFilename, "[unclosed")
	if err := clearBaseline(root, io.Discard); err == nil {
		t.Fatal("invalid ignore rule should stop clear")
	}
	after, _ := os.ReadFile(statePath)
	if !bytes.Equal(original, after) {
		t.Fatal("failed clear replaced the baseline")
	}
	put(t, root, stateDirectory+"/state.json", "broken JSON")
	if err := initialize(root, io.Discard); err == nil {
		t.Fatal("init must not silently replace a corrupt baseline")
	}
	if _, _, err := pendingChanges(root); err == nil {
		t.Fatal("invalid baseline should not produce an upload list")
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	put(t, root, "index.php", "site")
	put(t, outside, "private.txt", "not part of the site")
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Skip(err)
	}
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	state, _ := readBaseline(root)
	if len(state.Files) != 1 {
		t.Fatalf("symlink was followed: %#v", state.Files)
	}
}

type synchronizedOutput struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (output *synchronizedOutput) Write(data []byte) (int, error) {
	output.Lock()
	defer output.Unlock()
	return output.buffer.Write(data)
}

func (output *synchronizedOutput) text() string {
	output.Lock()
	defer output.Unlock()
	return output.buffer.String()
}

func waitFor(t *testing.T, check func() bool, output *synchronizedOutput) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher timed out:\n%s", output.text())
}

func TestWatcherResumesAndSeesClearInAnotherSession(t *testing.T) {
	root := t.TempDir()
	put(t, root, "index.php", "before")
	if err := initialize(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	put(t, root, "index.php", "already pending before watch starts")
	ctx, cancel := context.WithCancel(context.Background())
	output := &synchronizedOutput{}
	done := make(chan error, 1)
	go func() { done <- watch(ctx, root, 100*time.Millisecond, output) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return strings.Contains(output.text(), "MODIFIED  index.php") }, output)
	put(t, root, "index.php", "before")
	waitFor(t, func() bool { return strings.Contains(output.text(), "CLEARED   index.php") }, output)
	put(t, root, "index.php", "second edit")
	waitFor(t, func() bool { return strings.Count(output.text(), "MODIFIED  index.php") >= 2 }, output)
	put(t, root, "index.php", "third edit while still pending")
	waitFor(t, func() bool { return strings.Count(output.text(), "MODIFIED  index.php") >= 3 }, output)
	if err := clearBaseline(root, io.Discard); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(output.text(), "Baseline updated; 0 pending change(s).") }, output)
	put(t, root, "next.txt", "next deployment")
	waitFor(t, func() bool { return strings.Contains(output.text(), "ADDED     next.txt") }, output)
	if got := statuses(t, root); !reflect.DeepEqual(got, map[string]string{"next.txt": "ADDED"}) {
		t.Fatalf("watching should not mutate pending state: %#v", got)
	}
}

func TestOutputAndHelp(t *testing.T) {
	items := []change{{Status: "MODIFIED", Path: "asset with spaces.css", hash: "private hash"}, {Status: "DELETED", Path: "old.php"}}
	var out, errOut bytes.Buffer
	if err := printChanges(&out, &errOut, items, true, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "asset with spaces.css\n" || !strings.Contains(errOut.String(), "1 deletion(s)") {
		t.Fatalf("upload-only output: %q; stderr: %q", out.String(), errOut.String())
	}
	out.Reset()
	if err := printChanges(&out, &errOut, items, false, true); err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]string
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || len(decoded) != 2 || len(decoded[0]) != 2 {
		t.Fatalf("invalid JSON output or leaked internal data: %s", out.String())
	}
	for _, args := range [][]string{{}, {"--help"}, {"help", "watch"}, {"watch", "--help"}, {"--version"}} {
		out.Reset()
		if err := run(context.Background(), args, &out, &errOut); err != nil || out.Len() == 0 {
			t.Fatalf("help/version %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"help", "unknown"}, {"watch", "--interval", "0s"}, {"changes", "--files", "--json"}} {
		if err := run(context.Background(), args, &out, &errOut); err == nil {
			t.Fatalf("bad arguments accepted: %v", args)
		}
	}
}
