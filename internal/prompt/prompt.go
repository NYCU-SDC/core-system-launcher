// Package prompt provides dependency-free interactive input.
//
// No TUI library on purpose: the value of the launcher is that it runs anywhere
// straight away, and every dependency is one more thing that can break on
// someone else's machine.
package prompt

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	cyan   = "\033[36m"
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
)

var in = bufio.NewReader(os.Stdin)

// interrupt lets calls waiting on input be cancelled by Ctrl+C.
//
// A package-level variable rather than threading a context through every Ask:
// the program has exactly one interrupt source, and rewriting a dozen call
// sites for it does not pay for itself.
//
// Without this the program swallows Ctrl+C. main uses signal.NotifyContext, so
// the default terminate behaviour never happens, but bufio's blocking read
// ignores the context and the keypress appears to do nothing.
var interrupt <-chan struct{}

// WatchInterrupt makes subsequent input waits cancellable through ctx.
func WatchInterrupt(ctx context.Context) {
	interrupt = ctx.Done()
}

// Section prints a section heading.
func Section(title string) {
	fmt.Printf("\n%s%s%s\n", bold+cyan, title, reset)
}

// Info prints a normal message.
func Info(format string, a ...any) {
	fmt.Printf("  "+format+"\n", a...)
}

// Hint prints secondary detail.
func Hint(format string, a ...any) {
	fmt.Printf("  %s"+format+"%s\n", append([]any{dim}, append(a, reset)...)...)
}

// OK prints a success message.
func OK(format string, a ...any) {
	fmt.Printf("  %s✓%s "+format+"\n", append([]any{green, reset}, a...)...)
}

// Warn prints a warning.
func Warn(format string, a ...any) {
	fmt.Printf("  %s!%s "+format+"\n", append([]any{yellow, reset}, a...)...)
}

// Fail prints a failure.
func Fail(format string, a ...any) {
	fmt.Printf("  %s✗%s "+format+"\n", append([]any{red, reset}, a...)...)
}

func readLine() (string, error) {
	type result struct {
		s   string
		err error
	}
	// Read on a goroutine so the caller can wait on the interrupt at the same
	// time. On interrupt this goroutine stays blocked on stdin, which is fine
	// because the process is on its way out.
	ch := make(chan result, 1)
	go func() {
		s, err := in.ReadString('\n')
		ch <- result{strings.TrimSpace(s), err}
	}()

	select {
	case <-interrupt:
		return "", context.Canceled
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		return r.s, nil
	}
}

// Ask asks for a string. A non-empty def can be accepted by pressing Enter.
func Ask(question, def string) (string, error) {
	for {
		if def != "" {
			fmt.Printf("  %s %s(%s)%s ", question, dim, def, reset)
		} else {
			fmt.Printf("  %s ", question)
		}
		s, err := readLine()
		if err != nil {
			return "", err
		}
		if s == "" {
			if def != "" {
				return def, nil
			}
			Warn("這一項不能留空")
			continue
		}
		return s, nil
	}
}

// AskInt asks for an integer.
func AskInt(question string, def int) (int, error) {
	for {
		s, err := Ask(question, strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			Warn("請輸入數字")
			continue
		}
		return n, nil
	}
}

// Confirm asks a yes/no question.
func Confirm(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Printf("  %s %s(%s)%s ", question, dim, hint, reset)
		s, err := readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			Warn("請輸入 y 或 n")
		}
	}
}

// Select lets the user pick one option and returns its index.
func Select(question string, options []string, def int) (int, error) {
	fmt.Printf("  %s\n", question)
	for i, o := range options {
		fmt.Printf("    %s%d)%s %s\n", bold, i+1, reset, o)
	}
	for {
		n, err := AskInt("請選擇", def+1)
		if err != nil {
			return 0, err
		}
		if n < 1 || n > len(options) {
			Warn("請輸入 1 到 %d 之間的數字", len(options))
			continue
		}
		return n - 1, nil
	}
}

// Width returns how many terminal columns a string occupies.
//
// Neither len() (bytes) nor the rune count works: CJK characters and fullwidth
// punctuation take two columns in a monospaced terminal. fmt's %-24s pads by
// bytes, so mixed Chinese and English drifts out of alignment.
func Width(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isWide reports whether a rune is fullwidth. Only the ranges this CLI actually
// prints are covered: CJK plus fullwidth punctuation and symbols.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals, punctuation
		r >= 0x3041 && r <= 0x33FF,   // kana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth ASCII
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth symbols
		r >= 0x20000 && r <= 0x3FFFD: // CJK extension B and beyond
		return true
	}
	return false
}

// Pad pads a string to the given column width, keeping mixed-script text aligned.
func Pad(s string, width int) string {
	if n := width - Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// Pause waits for Enter, for when the user has to go do something elsewhere first.
func Pause(message string) error {
	fmt.Printf("  %s%s%s", dim, message, reset)
	_, err := readLine()
	return err
}
