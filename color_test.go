package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func unsetForTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, value)
		} else {
			os.Unsetenv(key)
		}
	})
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestColoredHumanOutputPreservesTextAndColumns(t *testing.T) {
	unsetForTest(t, "NO_COLOR")
	t.Setenv("SITEWATCH_COLOR", "always")
	items := []change{{Status: "ADDED", Path: "new.txt"}, {Status: "MODIFIED", Path: "asset with spaces.css"}, {Status: "DELETED", Path: "old.php"}}
	var colored, plain, errOut bytes.Buffer
	if err := printChanges(&colored, &errOut, items, false, false); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"\x1b[32mADDED", "\x1b[33mMODIFIED", "\x1b[31mDELETED"} {
		if !strings.Contains(colored.String(), prefix) {
			t.Fatalf("missing status color: %q", prefix)
		}
	}
	if !strings.Contains(statusLabel(&colored, "CLEARED"), "\x1b[36m") {
		t.Fatal("cleared entries should use cyan")
	}
	t.Setenv("SITEWATCH_COLOR", "never")
	if err := printChanges(&plain, &errOut, items, false, false); err != nil {
		t.Fatal(err)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	if ansi.ReplaceAllString(colored.String(), "") != plain.String() {
		t.Fatal("colors changed status text or alignment")
	}
	colored.Reset()
	plain.Reset()
	printHelp(&plain, "")
	t.Setenv("SITEWATCH_COLOR", "always")
	printHelp(&colored, "")
	if !strings.Contains(colored.String(), "\x1b[1;36mSitewatch") || ansi.ReplaceAllString(colored.String(), "") != plain.String() {
		t.Fatal("help formatting should preserve the original text")
	}
}

func TestMachineOutputRemainsPlainWhenColorsAreForced(t *testing.T) {
	unsetForTest(t, "NO_COLOR")
	t.Setenv("SITEWATCH_COLOR", "always")
	items := []change{{Status: "MODIFIED", Path: "file with spaces.txt"}, {Status: "DELETED", Path: "old.txt"}}
	var out, errOut bytes.Buffer
	if err := printChanges(&out, &errOut, items, true, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "file with spaces.txt\n" {
		t.Fatalf("upload paths contain formatting: %q", out.String())
	}
	out.Reset()
	if err := printChanges(&out, &errOut, items, false, true); err != nil {
		t.Fatal(err)
	}
	var decoded []change
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("JSON must remain plain: %q", out.String())
	}
}

func TestAutomaticColorsAndOptOut(t *testing.T) {
	unsetForTest(t, "NO_COLOR")
	t.Setenv("SITEWATCH_COLOR", "auto")
	t.Setenv("TERM", "xterm-256color")
	var buffer bytes.Buffer
	if colorEnabled(&buffer) {
		t.Fatal("redirected/buffered output should not have automatic colors")
	}
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if colorEnabled(file) {
		t.Fatal("a character device such as /dev/null is not a terminal")
	}
	t.Setenv("SITEWATCH_COLOR", "always")
	if !colorEnabled(&buffer) {
		t.Fatal("explicit color override should work")
	}
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(&buffer) {
		t.Fatal("NO_COLOR should override forced colors")
	}
	unsetForTest(t, "NO_COLOR")
	t.Setenv("SITEWATCH_COLOR", "never")
	if colorEnabled(&buffer) {
		t.Fatal("never should disable colors")
	}
	t.Setenv("SITEWATCH_COLOR", "auto")
	t.Setenv("TERM", "dumb")
	if colorEnabled(file) {
		t.Fatal("dumb terminals should not receive automatic colors")
	}
}
