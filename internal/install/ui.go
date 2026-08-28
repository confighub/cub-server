package install

import (
	"fmt"
	"io"
	"strings"
)

// UI is where an install reports what it is doing.
//
// An install creates things that outlive the command, some of them slowly, so
// silence between "install" and a prompt several minutes later is not an option.
// Every step announces itself before it runs rather than after, so an install
// that hangs says what it is hanging on.
type UI struct {
	Out io.Writer
}

func (u UI) step(format string, args ...any) {
	fmt.Fprintf(u.Out, "\n==> %s\n", fmt.Sprintf(format, args...))
}

func (u UI) detail(format string, args ...any) {
	fmt.Fprintf(u.Out, "    %s\n", fmt.Sprintf(format, args...))
}

func (u UI) warn(format string, args ...any) {
	fmt.Fprintf(u.Out, "    ! %s\n", fmt.Sprintf(format, args...))
}

// section prints a heading followed by an indented block, for the closing
// summary where the reader is looking for a command to copy rather than a log.
func (u UI) section(title string, lines ...string) {
	fmt.Fprintf(u.Out, "\n%s\n", title)
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			fmt.Fprintln(u.Out)
			continue
		}
		fmt.Fprintf(u.Out, "  %s\n", l)
	}
}
