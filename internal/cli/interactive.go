package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// stdinIsTTY reports whether stdin is an interactive terminal.
func stdinIsTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// prompter asks questions and reads answers. Prompts are written to out
// (stderr in production) so stdout stays machine-parseable.
type prompter struct {
	in       *bufio.Reader
	out      io.Writer
	stdinFD  int
	terminal bool
}

func newPrompter() *prompter {
	fd := int(os.Stdin.Fd())
	return &prompter{
		in:       bufio.NewReader(os.Stdin),
		out:      os.Stderr,
		stdinFD:  fd,
		terminal: term.IsTerminal(fd),
	}
}

func (p *prompter) ask(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// askSecret reads a line without echoing when stdin is a terminal.
func (p *prompter) askSecret(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	if p.terminal {
		data, err := term.ReadPassword(p.stdinFD)
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// confirmYN asks a yes/no question, defaulting to no.
func (p *prompter) confirmYN(prompt string) (bool, error) {
	ans, err := p.ask(prompt + " [y/N] ")
	if err != nil {
		return false, err
	}
	ans = strings.ToLower(ans)
	return ans == "y" || ans == "yes", nil
}

// selectOption renders a numbered menu and returns the chosen index, or -1
// when the user presses Enter and allowSkip is set.
func (p *prompter) selectOption(title string, options []string, allowSkip bool) (int, error) {
	fmt.Fprintln(p.out, title)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %2d) %s\n", i+1, o)
	}
	suffix := fmt.Sprintf("Choose [1-%d]", len(options))
	if allowSkip {
		suffix += ", or Enter to skip"
	}
	for {
		ans, err := p.ask(suffix + ": ")
		if err != nil {
			return 0, err
		}
		if ans == "" && allowSkip {
			return -1, nil
		}
		n, convErr := strconv.Atoi(ans)
		if convErr == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintf(p.out, "invalid choice %q\n", ans)
	}
}
