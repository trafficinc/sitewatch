package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	colorGreen  = "32"
	colorYellow = "33"
	colorRed    = "31"
	colorCyan   = "36"
	colorMuted  = "90"
	colorTitle  = "1;36"
)

func colorEnabled(out io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	switch os.Getenv("SITEWATCH_COLOR") {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := out.(*os.File)
	return ok && isTerminal(file)
}

func paint(out io.Writer, color, text string) string {
	if !colorEnabled(out) {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

func statusLabel(out io.Writer, status string) string {
	color := colorCyan
	switch status {
	case "ADDED":
		color = colorGreen
	case "MODIFIED":
		color = colorYellow
	case "DELETED":
		color = colorRed
	}
	// Pad before coloring so escape sequences do not affect column widths.
	return paint(out, color, fmt.Sprintf("%-9s", status))
}

func printHelpBlock(out io.Writer, text string) {
	for _, line := range strings.SplitAfter(text, "\n") {
		switch {
		case strings.HasPrefix(line, "Sitewatch"), strings.HasPrefix(line, "Usage:"), strings.HasPrefix(line, "Example:"):
			fmt.Fprint(out, paint(out, colorTitle, strings.TrimSuffix(line, "\n")))
			if strings.HasSuffix(line, "\n") {
				fmt.Fprint(out, "\n")
			}
		case strings.HasPrefix(line, "  "):
			content := line[2:]
			if separator := strings.Index(content, "  "); separator > 0 {
				fmt.Fprint(out, "  ", paint(out, colorCyan, content[:separator]), content[separator:])
			} else if strings.HasPrefix(content, "./sitewatch") {
				fmt.Fprint(out, "  ", paint(out, colorCyan, strings.TrimSuffix(content, "\n")))
				if strings.HasSuffix(content, "\n") {
					fmt.Fprint(out, "\n")
				}
			} else {
				fmt.Fprint(out, line)
			}
		default:
			fmt.Fprint(out, line)
		}
	}
}
