package display

import (
	"fmt"
	"os"
	"time"
)

// ────────────────────────────────────────────────────────────
// Exported color constants for use outside the display package
// ────────────────────────────────────────────────────────────

const (
	Reset  = reset
	Bold   = bold
	Dim    = dim
	Italic = italic

	Red     = red
	Green   = green
	Yellow  = yellow
	Blue    = blue
	Magenta = magenta
	Cyan    = cyan
	White   = white

	BrightRed     = brightRed
	BrightGreen   = brightGreen
	BrightYellow  = brightYellow
	BrightBlue    = brightBlue
	BrightMagenta = brightMagenta
	BrightCyan    = brightCyan
	BrightWhite   = brightWhite
)

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
		bold, brightCyan, step, total, reset,
		white, msg, reset,
	)
}

// StepDetail prints an indented detail line under a step.
func StepDetail(msg string) {
	fmt.Fprintf(os.Stdout, "        %s%s%s\n", dim+white, msg, reset)
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
	fmt.Fprintf(os.Stdout, "\r        %s%s%s\033[K", dim+white, msg, reset)
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
		dim, label, reset,
		bold+brightGreen, "", value, reset,
	)
}

// StepWarn prints a warning detail under a step.
func StepWarn(msg string) {
	fmt.Fprintf(os.Stdout, "        %s%s⚠ %s%s\n", yellow, bold, msg, reset)
}

// Info prints a general info message.
func Info(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%sℹ%s %s\n", brightBlue, bold, reset, msg)
}

// Success prints a green success message.
func Success(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%s✓%s %s\n", brightGreen, bold, reset, msg)
}

// Warn prints a yellow warning message.
func Warn(msg string) {
	fmt.Fprintf(os.Stdout, "  %s%s⚠%s %s%s%s\n", brightYellow, bold, reset, yellow, msg, reset)
}

// Error prints a red error message.
func ErrorMsg(msg string) {
	fmt.Fprintf(os.Stderr, "  %s%s✗%s %s%s%s\n", brightRed, bold, reset, red, msg, reset)
}

// Header prints a section header line.
func Header(msg string) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "  %s%s%s%s\n", bold, brightCyan, msg, reset)
	fmt.Fprintf(os.Stdout, "  %s%s%s%s\n", dim, cyan, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", reset)
}

// SubHeader prints a smaller section divider.
func SubHeader(msg string) {
	fmt.Fprintf(os.Stdout, "\n  %s%s%s%s\n", bold, brightYellow, msg, reset)
}

// KeyValue prints a labeled value.
func KeyValue(key string, value interface{}, valueColor string) {
	paddedKey := padRight(key, 18)
	fmt.Fprintf(os.Stdout, "    %s%s%s  %s%v%s\n", dim, paddedKey, reset, valueColor, value, reset)
}

// NextSteps prints an ordered list of next steps.
func NextSteps(steps []string) {
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "  %s%s📋 Next Steps%s\n", bold, brightYellow, reset)
	for i, step := range steps {
		fmt.Fprintf(os.Stdout, "    %s%s%d.%s %s\n", bold, brightWhite, i+1, reset, step)
	}
}

// FileCreated prints a file creation notice.
func FileCreated(path string) {
	fmt.Fprintf(os.Stdout, "    %s%s✓%s %s%s%s\n", brightGreen, bold, reset, dim+white, path, reset)
}

// DirCreated prints a directory creation notice.
func DirCreated(path string) {
	fmt.Fprintf(os.Stdout, "    %s%s📁%s %s%s%s\n", brightBlue, bold, reset, dim+white, path, reset)
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
		bold, methodColor, method, reset,
		white, path, reset,
		bold, statusColor, status, reset,
		dim, dur, reset,
		dim+white, remote, reset,
	)
}

func colorForMethod(method string) string {
	switch method {
	case "GET":
		return brightBlue
	case "POST":
		return brightGreen
	case "PUT", "PATCH":
		return brightYellow
	case "DELETE":
		return brightRed
	case "OPTIONS":
		return dim + white
	default:
		return white
	}
}

func colorForStatus(code int) string {
	switch {
	case code >= 500:
		return brightRed
	case code >= 400:
		return brightYellow
	case code >= 300:
		return brightCyan
	case code >= 200:
		return brightGreen
	default:
		return white
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
