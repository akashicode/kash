package display

import (
	"fmt"
	"os"
	"time"
)

// ────────────────────────────────────────────────────────────
// isTTY reports whether stdout is a terminal. Only the in-place progress line
// depends on it: carriage returns and erase-line escapes are noise in a log
// file or a CI transcript, where the same information is better left unwritten
// than written thousands of times.
var isTTY = func() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}()

// ────────────────────────────────────────────────────────────
// Log-level helpers (colored prefixes for CLI output)
// ────────────────────────────────────────────────────────────

// Step prints a build/init pipeline step like "  [1/5] Loading documents..."
func Step(step, total int, msg string) {
	fmt.Fprintf(os.Stdout, "  %s%s[%d/%d]%s %s%s%s\n",
		Bold, BrightCyan, step, total, reset,
		White, msg, reset,
	)
}

// StepDetail prints an indented detail line under a step.
func StepDetail(msg string) {
	fmt.Fprintf(os.Stdout, "        %s%s%s\n", Dim+White, msg, reset)
}

// Progress rewrites a single line in place with the work currently under way.
//
// A build spends most of its time inside one document, and printing a line per
// batch would bury the step output while printing nothing at all makes a stalled
// provider call look exactly like slow progress. This keeps a live line without
// filling the scrollback; call ProgressDone when the phase ends.
func Progress(msg string) {
	if !isTTY {
		return
	}
	fmt.Fprintf(os.Stdout, "\r        %s%s%s\033[K", Dim+White, msg, reset)
}

// ProgressDone clears the line Progress was writing to.
func ProgressDone() {
	if !isTTY {
		return
	}
	fmt.Fprint(os.Stdout, "\r\033[K")
}

// StepResult prints a success result for a step with a highlighted value.
func StepResult(label string, value interface{}) {
	fmt.Fprintf(os.Stdout, "        %s%s%s %s%s%v%s\n",
		Dim, label, reset,
		Bold+BrightGreen, "", value, reset,
	)
}

// StepWarn prints a warning detail under a step.
func StepWarn(msg string) {
	fmt.Fprintf(os.Stdout, "        %s%s⚠ %s%s\n", yellow, Bold, msg, reset)
}

// Info prints a general info message.
func Info(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%sℹ%s %s\n", brightBlue, Bold, reset, msg)
}

// Success prints a green success message.
func Success(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%s✓%s %s\n", BrightGreen, Bold, reset, msg)
}

// Warn prints a yellow warning message.
func Warn(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%s⚠%s %s%s%s\n", BrightYellow, Bold, reset, yellow, msg, reset)
}

// Header prints a section header line.
func Header(msg string) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "  %s%s%s%s\n", Bold, BrightCyan, msg, reset)
	fmt.Fprintf(os.Stdout, "  %s%s%s%s\n", Dim, cyan, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", reset)
}

// KeyValue prints a labeled value.
func KeyValue(key string, value interface{}, valueColor string) {
	paddedKey := padRight(key, 18)
	fmt.Fprintf(os.Stdout, "    %s%s%s  %s%v%s\n", Dim, paddedKey, reset, valueColor, value, reset)
}

// NextSteps prints an ordered list of next steps.
func NextSteps(steps []string) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "  %s%s📋 Next Steps%s\n", Bold, BrightYellow, reset)
	for i, step := range steps {
		fmt.Fprintf(os.Stdout, "    %s%s%d.%s %s\n", Bold, brightWhite, i+1, reset, step)
	}
}

// FileCreated prints a file creation notice.
func FileCreated(path string) {
	fmt.Fprintf(os.Stdout, "    %s%s✓%s %s%s%s\n", BrightGreen, Bold, reset, Dim+White, path, reset)
}

// ────────────────────────────────────────────────────────────
// HTTP Request Log — colorized request logging for the server
// ────────────────────────────────────────────────────────────

// LogRequest prints a colorized HTTP request log line to stdout.
func LogRequest(method, path string, status int, duration time.Duration, remote string) {
	methodColor := colorForMethod(method)
	statusColor := colorForStatus(status)
	dur := formatDuration(duration)

	fmt.Fprintf(os.Stdout, "  %s%s%-7s%s %s%-35s%s %s%s%d%s %s%s%s %s%s%s\n",
		Bold, methodColor, method, reset,
		White, path, reset,
		Bold, statusColor, status, reset,
		Dim, dur, reset,
		Dim+White, remote, reset,
	)
}

func colorForMethod(method string) string {
	switch method {
	case "GET":
		return brightBlue
	case "POST":
		return BrightGreen
	case "PUT", "PATCH":
		return BrightYellow
	case "DELETE":
		return brightRed
	case "OPTIONS":
		return Dim + White
	default:
		return White
	}
}

func colorForStatus(code int) string {
	switch {
	case code >= 500:
		return brightRed
	case code >= 400:
		return BrightYellow
	case code >= 300:
		return BrightCyan
	case code >= 200:
		return BrightGreen
	default:
		return White
	}
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dμs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}
