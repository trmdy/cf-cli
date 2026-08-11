// Command lint walks the full cf command tree and enforces the mechanical
// rules from docs/STYLE.md. It exists so that style violations from parallel
// porcelain development fail CI instead of relying on reviewer attention.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trmdy/cf-cli/internal/cli"
)

var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// destructiveVerbs are porcelain command names that must support --force so
// they remain scriptable without a TTY confirmation.
var destructiveVerbs = map[string]bool{"delete": true, "purge": true, "remove": true, "destroy": true}

type linter struct {
	problems []string
}

func (l *linter) errorf(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

func main() {
	root := cli.NewRootCmd()
	l := &linter{}
	walk(l, root, nil, false)
	if len(l.problems) > 0 {
		for _, p := range l.problems {
			fmt.Fprintln(os.Stderr, "lint:", p)
		}
		fmt.Fprintf(os.Stderr, "lint: %d problem(s)\n", len(l.problems))
		os.Exit(1)
	}
	fmt.Println("lint: ok")
}

func walk(l *linter, cmd *cobra.Command, path []string, inAPI bool) {
	name := cmd.Name()
	full := strings.Join(append(path, name), " ")
	inAPI = inAPI || name == "api"

	if name != "cf" && !kebab.MatchString(name) {
		l.errorf("%s: command name %q is not kebab-case", full, name)
	}
	if strings.TrimSpace(cmd.Short) == "" {
		l.errorf("%s: empty Short description", full)
	}
	if len(cmd.Short) > 100 {
		l.errorf("%s: Short description over 100 chars", full)
	}

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !kebab.MatchString(f.Name) {
			l.errorf("%s: flag --%s is not kebab-case", full, f.Name)
		}
		if strings.TrimSpace(f.Usage) == "" {
			l.errorf("%s: flag --%s has no usage text", full, f.Name)
		}
	})

	// Porcelain-only rules (generated api commands are exempt: their shape
	// is the generator's responsibility, checked by contract tests).
	if !inAPI && cmd.RunE != nil {
		if destructiveVerbs[name] && cmd.Flags().Lookup("force") == nil {
			l.errorf("%s: destructive command must support --force", full)
		}
		if name == "create" && !strings.Contains(cmd.Long, "cf ") && !strings.Contains(cmd.Example, "cf ") {
			l.errorf("%s: create commands must show at least one example (in Long or Example)", full)
		}
	}

	for _, sub := range cmd.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		// Descending into the generated api tree costs nothing and validates
		// generator output names/flags too.
		walk(l, sub, append(path, name), inAPI)
	}
}
