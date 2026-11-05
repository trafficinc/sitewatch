# Sitewatch

A standalone local deployment tracker for sites updated with SFTP (manually) designed for brochure sites or quick sites where you don't need a bunch of fluff (great for shared hosting sites). It keeps a content-hash baseline and lists what has changed since you last accepted an
upload. It does not use Git history (so you can use git seperately without affecting this), contact a server, or upload anything.

## Use in this project

From the project root:

```sh
./sitewatch init
./sitewatch watch
```

Make your edits. Stop watching with Ctrl+C, or use another terminal to get the
complete list:

```sh
./sitewatch changes
```

Example:

```text
MODIFIED  index.php
ADDED     assets/new photo.jpg
DELETED   old-page.php
```

Upload the added and modified files with your SFTP client. Remove deleted files
from the server yourself. When all of those actions are complete and your edits
are saved, accept the current local files:

```sh
./sitewatch clear
./sitewatch watch
```

If a watcher is already running, it notices `clear` and keeps watching against
the new baseline. You do not need to restart it.

## Workflow

Open two terminals in your project directory:

1. In the first terminal, run ./sitewatch watch and leave it running to see file changes as you work.
2. When you’re ready to upload, run ./sitewatch changes in the second terminal to see the complete list of pending changes.
3. Upload added and modified files with your SFTP client, and remove deleted files from the server.
4. After completing the uploads and deletions, run ./sitewatch clear in the second terminal to reset the checklist.

## Commands

| Command | Purpose |
| --- | --- |
| `sitewatch init` | Create configuration and record the current local files |
| `sitewatch watch` | Print changes live; default scan interval is one second |
| `sitewatch watch --interval 2s` | Use a different scan interval |
| `sitewatch changes` | List all pending uploads and deletions |
| `sitewatch changes --files` | Print added/modified paths only; note deletions on stderr |
| `sitewatch changes --json` | Print structured status/path objects, including deletions |
| `sitewatch clear` | Accept all current files as the new baseline after uploading |
| `sitewatch help` | Show the help menu |
| `sitewatch help watch` | Show help for a command |
| `sitewatch --version` | Print the executable version |

`--help` also works on individual commands. After initialization, commands find
the project root from its subdirectories. `init` always initializes the current
directory; run it at the intended project root.

## Terminal colors

On macOS and Linux, help and status messages use colors automatically in a
terminal: green for added files and successful actions, yellow for modified
files, red for deleted files and errors, and cyan for cleared entries and watcher
updates. Status words remain present so color is not required to interpret them.

Redirected output is plain by default. JSON and `changes --files` filenames stay
plain even when colors are forced.

```sh
NO_COLOR=1 ./sitewatch watch             # Disable colors
SITEWATCH_COLOR=never ./sitewatch help   # Also disable colors
SITEWATCH_COLOR=always ./sitewatch help  # Force ANSI colors
```

`NO_COLOR` takes precedence over forced colors. A `TERM=dumb` terminal disables
automatic colors. On other operating systems, use `SITEWATCH_COLOR=always` if
your terminal supports ANSI colors.

## What the baseline means

- `init` records the files currently on your computer. It cannot determine which
  of your existing edits have already been uploaded. Use it before the next set
  of edits, or reconcile any existing pending uploads yourself first.
- Repeating `init` preserves the baseline and pending changes.
- `changes` scans current content even if `watch` has never been running.
- Stopping or restarting `watch` does not reset the baseline.
- Reverting a file to its baseline contents removes it from the upload list.
- Adding and then deleting a file before uploading also removes it from the list.
- Renames appear as a deletion and an addition.
- `clear` accepts everything currently present. Finish uploading and pause edits
  before running it; the tool does not verify that an upload succeeded.

The state contains relative paths and SHA-256 hashes, not copies of your files.
It lives in `.sitewatch/state.json`. Baseline updates use a temporary file and
rename so readers see a complete state file. Scan errors leave the saved baseline
untouched. The watcher reports errors and retries on subsequent scans.

Only regular files are tracked. Symlinks are not followed. Timestamps and file
permission changes alone are not deployment changes; file contents determine the
upload list. All included files are hashed on each scan. Increase the interval
or exclude large local-only directories when needed.

## Local-only files

Edit `.sitewatchignore` at the project root. `init` creates it if missing and
copies the existing `.gitignore` rules into it once. They are independent after
that: edit `.sitewatchignore` to control the upload checklist. An existing
`.sitewatchignore` is preserved.

Example:

```gitignore
/blog/
/storage/
/includes/config.local.php
*.sqlite
*.log
/tools/sitewatch/
```

The supported pattern syntax is the common Git-ignore style:

- `/folder/` excludes a directory at the project root and its contents.
- `folder/` matches that directory name at any depth.
- `*.log` matches file or directory names at any depth.
- `assets/**/*.map` includes zero or more directory levels between the slashes.
- `?` matches one non-slash character; character classes such as `[0-9]` work.
- `!pattern` reincludes a match; later matching rules take precedence.
- An excluded parent directory must be reincluded before its children can be
  watched. This allows ignored directories to be skipped during scanning.
- Blank lines and lines starting with `#` are ignored. Leading and trailing
  whitespace is trimmed. Use `\#` or `\!` for a literal leading character.

Ignore changes take effect on the next scan. Excluding a previously tracked file
does not incorrectly list it as deleted. Removing an exclusion can list newly
included files as added until you accept the next baseline.

The executable, `.sitewatchignore`, `.sitewatch/`, `.git/`, and temporary state
files are always excluded from the deployment checklist. The default ignore file
also excludes `.gitignore` and this tool's source directory. Keep
`.sitewatchignore` in Git if you want to share project exclusion rules.

`init` appends these missing rules to `.gitignore`, preserving existing rules:

```gitignore
# Sitewatch local tooling
/sitewatch
/sitewatch.exe
/.sitewatch/
# End Sitewatch local tooling
```

## Copy to another project

Copy only the executable into the next project and initialize it there:

```sh
cp /path/to/this/project/sitewatch /path/to/another/project/sitewatch
cd /path/to/another/project
./sitewatch init
```

Review that project's `.sitewatchignore` before editing. Each project has its own
baseline. Keep this source directory as the maintained copy and rebuild the
executable when you update the tool.

The supplied executable targets Apple Silicon macOS. A compiled executable must
match the operating system and CPU of the computer running it. No Go installation
is needed to run a matching binary.

## Build and test

Go 1.22 or newer is required for development. There are no external dependencies.
From this source directory:

```sh
go test -race ./...
go build -trimpath -ldflags="-s -w" -o ../../sitewatch .
```

For another machine, set the appropriate Go build target, for example:

```sh
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o sitewatch-intel .
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o sitewatch-linux .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o sitewatch.exe .
```

Build additional binaries outside the deployment tree or exclude their filenames
in `.sitewatchignore`.
