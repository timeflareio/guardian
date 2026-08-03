// Package ui is the guardian's terminal presentation layer: colour, banners,
// indentation and prompts. It is the only package permitted to write to the
// process's output, which `make verify-boundaries` checks — everything else
// returns values or errors and lets a caller decide how to say it.
//
// A Printer carries its own writers rather than reaching for os.Stdout. That is
// what makes the command layer testable: a test constructs a Printer over a
// buffer and asserts on what a command said, which was impossible while every
// helper called fmt.Printf directly.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"golang.org/x/term"
)

// Indentation constants for consistent formatting
const (
	Indent1 = "   "    // 3 spaces - standard indentation
	Indent2 = "      " // 6 spaces - double indentation
)

// Printer writes the guardian's operator-facing output.
type Printer struct {
	out io.Writer
	in  io.Reader

	header  *color.Color
	success *color.Color
	failure *color.Color
	note    *color.Color
	command *color.Color
}

// New builds a Printer over the given writers. Colour is enabled unless the
// output is not a terminal or NO_COLOR is set: the daemon's startup banner ends
// up in a container log, where escape codes are noise rather than emphasis.
func New(out io.Writer, in io.Reader) *Printer {
	p := &Printer{
		out:     out,
		in:      in,
		header:  color.New(color.FgCyan, color.Bold),
		success: color.New(color.FgGreen),
		failure: color.New(color.FgRed),
		note:    color.New(color.FgYellow),
		command: color.New(color.FgGreen, color.Bold),
	}
	if !colourWanted(out) {
		for _, c := range []*color.Color{p.header, p.success, p.failure, p.note, p.command} {
			c.DisableColor()
		}
	}
	return p
}

// colourWanted reports whether escape codes belong in this output. NO_COLOR is
// honoured for any non-empty value, per the informal convention.
func colourWanted(out io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Message helpers — these cover most use cases.

func (p *Printer) Header(format string, args ...any) {
	_, _ = p.header.Fprintln(p.out, fmt.Sprintf(format, args...))
}

func (p *Printer) Success(format string, args ...any) {
	_, _ = p.success.Fprintf(p.out, "✅ %s\n", fmt.Sprintf(format, args...))
}

func (p *Printer) Error(format string, args ...any) {
	_, _ = p.failure.Fprintf(p.out, "❌ %s\n", fmt.Sprintf(format, args...))
}

func (p *Printer) Warning(format string, args ...any) {
	_, _ = p.failure.Fprintf(p.out, "⚠️  %s\n", fmt.Sprintf(format, args...))
}

func (p *Printer) Note(format string, args ...any) {
	_, _ = p.note.Fprintf(p.out, "💡 %s\n", fmt.Sprintf(format, args...))
}

func (p *Printer) Step(format string, args ...any) {
	_, _ = p.header.Fprintln(p.out, fmt.Sprintf(format, args...))
}

// Inline content helpers (no newline, no prefix).

func (p *Printer) Command(format string, args ...any) {
	_, _ = p.command.Fprint(p.out, fmt.Sprintf(format, args...))
}

func (p *Printer) Path(format string, args ...any) {
	_, _ = p.header.Fprint(p.out, fmt.Sprintf(format, args...))
}

func (p *Printer) Value(format string, args ...any) {
	_, _ = p.note.Fprint(p.out, fmt.Sprintf(format, args...))
}

func (p *Printer) Key(format string, args ...any) {
	_, _ = p.command.Fprint(p.out, fmt.Sprintf(format, args...))
}

// Unstyled helpers.

func (p *Printer) Text(text string) {
	_, _ = fmt.Fprint(p.out, text)
}

func (p *Printer) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, format, args...)
}

func (p *Printer) TextLn(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, format+"\n", args...)
}

func (p *Printer) EmptyLine() {
	_, _ = fmt.Fprintln(p.out)
}

// Separator prints a heading underlined to its own visible width.
func (p *Printer) Separator(text string) {
	_, _ = p.header.Fprintln(p.out, text)
	_, _ = p.header.Fprintln(p.out, strings.Repeat("═", len(StripANSI(text))))
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// StripANSI removes colour escape codes, so a caller aligning columns measures
// the width a reader sees rather than the width of the bytes.
func StripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// PromptInput prompts for a line of input and returns it trimmed.
func (p *Printer) PromptInput(prompt string) string {
	p.Text(prompt)
	input, _ := bufio.NewReader(p.in).ReadString('\n')
	return strings.TrimSpace(input)
}

// Confirm asks a yes/no question, defaulting to no. Anything other than an
// explicit yes — including a read error, which is what a closed or
// non-interactive stdin gives — is a no.
func (p *Printer) Confirm(message string) bool {
	p.Printf("%s [y/N]: ", message)
	response, err := bufio.NewReader(p.in).ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// ReadPassword reads a secret without echoing it. When the input is a terminal
// it suppresses echo and restores the terminal if interrupted; otherwise — a
// pipe, a here-string, a test — it reads a line.
func (p *Printer) ReadPassword() ([]byte, error) {
	f, isFile := p.in.(*os.File)
	if !isFile || !term.IsTerminal(int(f.Fd())) {
		return bufio.NewReader(p.in).ReadBytes('\n')
	}
	fd := int(f.Fd())

	// Restore the terminal if the operator interrupts mid-entry, rather than
	// leaving the shell with echo disabled.
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	oldState, err := term.GetState(fd)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get terminal state")
	}

	go func() {
		select {
		case <-sigs:
			_ = term.Restore(fd, oldState) // Error ignored during signal handling
			p.TextLn("\n\n⚠️  Password entry cancelled.")
			os.Exit(1)
		case <-done:
			return
		}
	}()

	password, err := term.ReadPassword(fd)

	signal.Stop(sigs)
	done <- true

	if err != nil {
		return nil, err
	}

	// term.ReadPassword swallows the newline the operator typed.
	p.EmptyLine()

	return password, nil
}

// NewPassphrase reads and confirms a new passphrase, re-prompting until it is
// non-empty and typed the same way twice.
func (p *Printer) NewPassphrase(purpose string) (string, error) {
	for {
		p.Text(Indent1 + "🔑 Choose a passphrase for the " + purpose + ": ")
		first, err := p.ReadPassword()
		if err != nil {
			return "", err
		}
		passphrase := strings.TrimSpace(string(first))
		if passphrase == "" {
			p.Warning("Passphrase cannot be empty")
			continue
		}
		p.Text(Indent1 + "🔁 Confirm passphrase: ")
		second, err := p.ReadPassword()
		if err != nil {
			return "", err
		}
		if passphrase != strings.TrimSpace(string(second)) {
			p.Warning("Passphrases do not match — try again")
			continue
		}
		return passphrase, nil
	}
}
