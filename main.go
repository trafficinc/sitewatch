package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var version = "1.1.0"

const stateDirectory = ".sitewatch"
const ignoreFilename = ".sitewatchignore"
const stateVersion = 1

type baseline struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	SavedAt time.Time         `json:"saved_at"`
	Files   map[string]string `json:"files"`
}

type change struct {
	Status string `json:"status"`
	Path   string `json:"path"`
	// Used only to notice another edit while a file is already pending.
	hash string
}

type ignoreRule struct {
	pattern   *regexp.Regexp
	negate    bool
	directory bool
}

type exclusions struct {
	rules      []ignoreRule
	executable string
}

func main() {
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "watch" {
		watchContext, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		ctx = watchContext
	}
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, paint(os.Stderr, colorRed, "sitewatch:"), err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		printHelp(out, "")
		return nil
	}
	command := args[0]
	if command == "--version" || command == "version" {
		fmt.Fprintln(out, "sitewatch", version)
		return nil
	}
	if command == "help" || command == "--help" || command == "-h" {
		topic := ""
		if len(args) > 1 {
			topic = args[1]
		}
		if topic != "" && !knownCommand(topic) {
			return fmt.Errorf("unknown help topic %q", topic)
		}
		printHelp(out, topic)
		return nil
	}
	if !knownCommand(command) {
		return fmt.Errorf("unknown command %q; run sitewatch help", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.Usage = func() { printHelp(out, command) }
	var filesOnly, asJSON bool
	interval := time.Second
	if command == "changes" {
		flags.BoolVar(&filesOnly, "files", false, "print only added/modified paths for uploading")
		flags.BoolVar(&asJSON, "json", false, "print structured changes as JSON")
	}
	if command == "watch" {
		flags.DurationVar(&interval, "interval", time.Second, "polling interval, e.g. 500ms or 2s")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; run sitewatch help %s", flags.Arg(0), command)
	}
	if filesOnly && asJSON {
		return errors.New("use either --files or --json")
	}
	if interval < 100*time.Millisecond {
		return errors.New("watch interval must be at least 100ms")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if command == "init" {
		return initialize(cwd, out)
	}
	root, err := findProject(cwd)
	if err != nil {
		return err
	}
	switch command {
	case "changes":
		_, pending, err := pendingChanges(root)
		if err != nil {
			return err
		}
		return printChanges(out, errOut, pending, filesOnly, asJSON)
	case "clear":
		return clearBaseline(root, out)
	case "watch":
		return watch(ctx, root, interval, out)
	}
	return nil
}

func knownCommand(command string) bool {
	return command == "init" || command == "watch" || command == "changes" || command == "clear"
}

func printHelp(out io.Writer, topic string) {
	if topic == "" {
		printHelpBlock(out, `Sitewatch — a local SFTP upload checklist

Usage: sitewatch <command> [options]

  init                  Initialize the current folder and record its files
  watch                 Print changes live; Ctrl+C stops watching
  changes               List pending added, modified, and deleted files
  changes --files       Print added/modified paths only, one per line
  changes --json        Print all pending changes as JSON
  clear                 After uploading, accept current files as the baseline
  help [command]        Show help and examples
  --version             Show the executable version

Example:
  ./sitewatch init
  ./sitewatch watch
  ./sitewatch changes   # Run in another terminal, or stop watch first
  # Upload the listed files with your SFTP client; handle deletions separately.
  ./sitewatch clear

Edit .sitewatchignore to exclude local-only files. Init seeds it from .gitignore
once; the two files are independent afterward. Init adds the executable and
.sitewatch/ to .gitignore. The tracker never connects to a server or uploads files.
Colors are automatic in terminals. Set NO_COLOR=1 to disable them, or
SITEWATCH_COLOR=always to force colors. JSON and --files output stay plain.
`)
		return
	}
	switch topic {
	case "init":
		printHelpBlock(out, `Usage: sitewatch init

Initialize the current directory. Creates .sitewatchignore if missing, appends
local-tool exclusions to .gitignore, and saves a baseline in .sitewatch/.
Existing ignore files are preserved. Repeating init never resets a baseline.
The current local files are the starting point; server files are not checked.
`)
	case "watch":
		printHelpBlock(out, `Usage: sitewatch watch [--interval 1s]

Show pending changes immediately, then scan for content changes at the given
interval (minimum 100ms). Ctrl+C stops watching without losing pending changes.
ADDED, MODIFIED, and DELETED are pending SFTP actions. CLEARED means a path no
longer differs from the baseline or has been excluded. A clear in another
terminal is detected automatically. There is no background service.
`)
	case "changes":
		printHelpBlock(out, `Usage: sitewatch changes [--files | --json]

Compare current file contents with the saved baseline, even when watch is off.
Paths are relative to the project root. Renames appear as a deletion and addition.
--files lists only files to upload; pending deletions are noted on stderr.
--json returns objects with status and path, including deleted paths.
`)
	case "clear":
		printHelpBlock(out, `Usage: sitewatch clear

After uploading all pending files and handling deletions, record the current
local files as the new baseline. This clears the entire pending list. It does
not check the server. Finish uploads and pause edits before running this command.
A running watcher notices the reset and continues watching against the new state.
`)
	}
}

func findProject(start string) (string, error) {
	for dir := start; ; dir = filepath.Dir(dir) {
		_, err := os.Stat(filepath.Join(dir, stateDirectory, "state.json"))
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", errors.New("not initialized; run sitewatch init from the project root")
}

func initialize(root string, out io.Writer) error {
	if _, err := os.Stat(filepath.Join(root, stateDirectory, "state.json")); err == nil {
		if _, err := readBaseline(root); err != nil {
			return err
		}
		fmt.Fprintln(out, paint(out, colorGreen, "Already initialized; baseline preserved."), "Run sitewatch changes to see pending files.")
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := createIgnoreFile(root); err != nil {
		return err
	}
	if err := updateGitignore(root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, stateDirectory), 0700); err != nil {
		return err
	}
	// Defense in depth if someone accidentally copies local state to Apache.
	if err := writeIfMissing(filepath.Join(root, stateDirectory, ".htaccess"), []byte("Require all denied\n"), 0600); err != nil {
		return err
	}
	files, err := snapshot(root)
	if err != nil {
		return err
	}
	if err := saveBaseline(root, files); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s\nRecorded %d files. Future edits will appear in sitewatch changes.\n", paint(out, colorGreen, "Initialized"), root, len(files))
	fmt.Fprintln(out, "Exclusions: .sitewatchignore. Local executable/state exclusions added to .gitignore.")
	return nil
}

func createIgnoreFile(root string) error {
	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := `# Sitewatch deployment exclusions. Edit these independently of .gitignore.
# Patterns support *, ?, **, character classes, /folder/, and ! reinclusion.
# Excluded directories must be reincluded before their children can be watched.

# Local project/tool metadata and the maintained tool source.
/.gitignore
/tools/sitewatch/
.DS_Store
`
	if len(gitignore) != 0 {
		text += "\n# Rules copied from .gitignore at initialization (not kept in sync).\n"
		text += strings.TrimRight(string(gitignore), "\r\n") + "\n"
	}
	return writeIfMissing(filepath.Join(root, ignoreFilename), []byte(text), 0644)
}

func updateGitignore(root string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, rule := range []string{"/sitewatch", "/sitewatch.exe", "/.sitewatch/"} {
		if !existing[rule] {
			missing = append(missing, rule)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n# Sitewatch local tooling\n" + strings.Join(missing, "\n") + "\n# End Sitewatch local tooling\n"
	return atomicWrite(path, []byte(text), 0644)
}

func writeIfMissing(path string, data []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func readBaseline(root string) (baseline, error) {
	var state baseline
	data, err := os.ReadFile(filepath.Join(root, stateDirectory, "state.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("invalid .sitewatch/state.json: %w", err)
	}
	if state.Version != stateVersion || state.ID == "" || state.Files == nil {
		return state, errors.New("unsupported or incomplete .sitewatch/state.json; baseline was not changed")
	}
	return state, nil
}

func saveBaseline(root string, files map[string]string) error {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	state := baseline{Version: stateVersion, ID: hex.EncodeToString(id), SavedAt: time.Now().UTC(), Files: files}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(root, stateDirectory, "state.json"), append(data, '\n'), 0600)
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".sitewatch-tmp-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func loadExclusions(root string) (exclusions, error) {
	var result exclusions
	executable, err := os.Executable()
	if err == nil {
		if rel, err := filepath.Rel(root, executable); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			result.executable = filepath.ToSlash(rel)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, ignoreFilename))
	if err != nil {
		return result, fmt.Errorf("read %s: %w", ignoreFilename, err)
	}
	for index, line := range strings.Split(string(data), "\n") {
		rule, skip, err := parseIgnoreRule(line)
		if err != nil {
			return result, fmt.Errorf("%s:%d: %w", ignoreFilename, index+1, err)
		}
		if !skip {
			result.rules = append(result.rules, rule)
		}
	}
	return result, nil
}

func parseIgnoreRule(line string) (ignoreRule, bool, error) {
	var rule ignoreRule
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return rule, true, nil
	}
	if strings.HasPrefix(line, "!") {
		rule.negate = true
		line = strings.TrimPrefix(line, "!")
	} else if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	}
	rule.directory = strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return rule, false, errors.New("empty ignore pattern")
	}
	pattern, err := globRegex(line)
	if err != nil {
		return rule, false, err
	}
	prefix := "^"
	if !anchored {
		prefix = "(?:^|/)"
	}
	rule.pattern, err = regexp.Compile(prefix + pattern + "$")
	return rule, false, err
}

func globRegex(glob string) (string, error) {
	var result strings.Builder
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					result.WriteString("(?:.*/)?")
					i++
				} else {
					result.WriteString(".*")
				}
			} else {
				result.WriteString("[^/]*")
			}
		case '?':
			result.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("unclosed character class in %q", glob)
			}
			end += i + 1
			class := glob[i+1 : end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			result.WriteString("[" + class + "]")
			i = end
		case '\\':
			if i+1 == len(glob) {
				return "", errors.New("trailing escape in ignore pattern")
			}
			i++
			result.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		default:
			result.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	return result.String(), nil
}

func (rules exclusions) ignored(path string, isDirectory bool) bool {
	// These are local bookkeeping files regardless of user patterns.
	if path == "sitewatch" || path == "sitewatch.exe" || path == ignoreFilename || path == rules.executable || strings.HasPrefix(filepath.Base(path), ".sitewatch-tmp-") {
		return true
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == ".git" || part == stateDirectory {
			return true
		}
	}
	// Excluded parent directories stay excluded until explicitly reincluded,
	// matching the usual .gitignore directory semantics.
	for index := range parts {
		candidate := strings.Join(parts[:index+1], "/")
		directory := index < len(parts)-1 || isDirectory
		ignored := false
		for _, rule := range rules.rules {
			if (!rule.directory || directory) && rule.pattern.MatchString(candidate) {
				ignored = !rule.negate
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

func snapshot(root string) (map[string]string, error) {
	rules, err := loadExclusions(root)
	if err != nil {
		return nil, err
	}
	return scan(root, rules)
}

func scan(root string, rules exclusions) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if path == root {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rules.ignored(rel, entry != nil && entry.IsDir()) {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if err := errors.Join(copyErr, statErr, closeErr); err != nil {
			return err
		}
		if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("%s changed during the scan; retry after the file is saved", rel)
		}
		files[rel] = hex.EncodeToString(hasher.Sum(nil))
		return nil
	})
	return files, err
}

func pendingChanges(root string) (baseline, []change, error) {
	state, err := readBaseline(root)
	if err != nil {
		return state, nil, err
	}
	rules, err := loadExclusions(root)
	if err != nil {
		return state, nil, err
	}
	current, err := scan(root, rules)
	if err != nil {
		return state, nil, err
	}
	pending := make([]change, 0)
	for path, hash := range current {
		if old, exists := state.Files[path]; !exists {
			pending = append(pending, change{Status: "ADDED", Path: path, hash: hash})
		} else if old != hash {
			pending = append(pending, change{Status: "MODIFIED", Path: path, hash: hash})
		}
	}
	for path := range state.Files {
		if _, exists := current[path]; !exists && !rules.ignored(path, false) {
			pending = append(pending, change{Status: "DELETED", Path: path})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Path < pending[j].Path })
	return state, pending, nil
}

func printChanges(out, errOut io.Writer, pending []change, filesOnly, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(pending)
	}
	if filesOnly {
		deletions := 0
		for _, item := range pending {
			if item.Status == "DELETED" {
				deletions++
			} else {
				fmt.Fprintln(out, item.Path)
			}
		}
		if deletions > 0 {
			fmt.Fprintln(errOut, paint(errOut, colorYellow, fmt.Sprintf("%d deletion(s) also pending; run sitewatch changes to see them.", deletions)))
		}
		return nil
	}
	if len(pending) == 0 {
		fmt.Fprintln(out, paint(out, colorGreen, "No pending changes."))
		return nil
	}
	for _, item := range pending {
		fmt.Fprintf(out, "%s %s\n", statusLabel(out, item.Status), item.Path)
	}
	fmt.Fprintln(out, "\n"+paint(out, colorMuted, fmt.Sprintf("%d pending change(s). Upload added/modified files; handle deletions on the server.", len(pending))))
	return nil
}

func clearBaseline(root string, out io.Writer) error {
	files, err := snapshot(root)
	if err != nil {
		return err
	}
	if err := saveBaseline(root, files); err != nil {
		return err
	}
	fmt.Fprintln(out, paint(out, colorGreen, fmt.Sprintf("Baseline reset to %d current files. No pending changes.", len(files))))
	return nil
}

func watch(ctx context.Context, root string, interval time.Duration, out io.Writer) error {
	fmt.Fprintf(out, "%s %s (every %s). Ctrl+C stops; baseline is preserved.\n", paint(out, colorCyan, "Watching"), root, interval)
	last := make(map[string]change)
	lastID := ""
	var lastError string
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		state, pending, err := pendingChanges(root)
		if err != nil {
			if err.Error() != lastError {
				fmt.Fprintf(out, "%s %s\n", paint(out, colorRed, "Scan failed; pending state preserved:"), err)
				lastError = err.Error()
			}
		} else {
			if lastError != "" {
				fmt.Fprintln(out, paint(out, colorGreen, "Scanning resumed."))
				lastError = ""
			}
			if lastID != "" && state.ID != lastID {
				fmt.Fprintln(out, paint(out, colorCyan, fmt.Sprintf("Baseline updated; %d pending change(s).", len(pending))))
				last = make(map[string]change)
			}
			current := make(map[string]change)
			for _, item := range pending {
				current[item.Path] = item
				if previous, exists := last[item.Path]; !exists || previous != item {
					fmt.Fprintf(out, "%s %s\n", statusLabel(out, item.Status), item.Path)
				}
			}
			var cleared []string
			for path := range last {
				if _, exists := current[path]; !exists {
					cleared = append(cleared, path)
				}
			}
			sort.Strings(cleared)
			for _, path := range cleared {
				fmt.Fprintf(out, "%s %s (no longer pending)\n", statusLabel(out, "CLEARED"), path)
			}
			if lastID == "" && len(pending) == 0 {
				fmt.Fprintln(out, paint(out, colorGreen, "No pending changes."))
			}
			lastID, last = state.ID, current
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, paint(out, colorMuted, "Stopped watching. Pending changes are preserved."))
			return nil
		case <-ticker.C:
		}
	}
}
