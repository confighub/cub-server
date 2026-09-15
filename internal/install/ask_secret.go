package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// askSecret reads a value that should not be left on the screen.
//
// The rest of this installer works hard to keep credentials off terminals --
// `keycloak open` puts a password on the clipboard rather than printing it --
// and a prompt that echoes one back undoes that: it is in the scrollback, in
// whatever records the session, and over the shoulder of whoever is watching.
//
// Echo is only suppressible on a real terminal. Reading from a pipe (a script,
// a test) falls back to the ordinary line read, which is correct rather than a
// compromise: there is nothing to hide from in that case, and refusing would
// make the command unscriptable.
func askSecret(r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprintf(out, "%s: ", prompt)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}

	// Straight from the terminal rather than through r: the bufio.Reader may
	// already have buffered the bytes being typed, and reading around it is what
	// keeps them out of the echoed line.
	typed, err := term.ReadPassword(fd)
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", strings.TrimSpace(prompt), err)
	}
	return strings.TrimSpace(string(typed)), nil
}
