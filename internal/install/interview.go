package install

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// Interactive mode fills the same Options struct the flags fill, and then the
// same Run consumes it. So there is no interactive-only path that can work when
// the scripted one does not, which is the failure mode of installers that grow
// a wizard alongside their flags.
//
// Only questions whose answer changes the outcome are asked. Anything with a
// defensible default is defaulted and reported at the end, because an install
// someone is evaluating should be short, and a prompt that always takes its
// default is a keystroke tax rather than a choice.

// Interview asks what it needs and fills o. A flag already set on the command
// line is taken as answered and not asked about.
func Interview(in io.Reader, out io.Writer, o *Options) error {
	r := bufio.NewReader(in)

	fmt.Fprintln(out, "This installs a ConfigHub server you own, and signs you in to it.")

	if o.Target == "" {
		answer, err := choose(r, out,
			"Where should it run?",
			[]string{
				"a local cluster, created now with kind (needs Docker)",
				"a Kubernetes cluster I already have",
			}, 1)
		if err != nil {
			return err
		}
		if answer == 1 {
			o.Target = TargetKind
		} else {
			o.Target = TargetContext
		}
	}

	switch o.Target {
	case TargetKind:
		if o.ClusterName == "" {
			name, err := ask(r, out, "Name for the cluster", DefaultClusterName)
			if err != nil {
				return err
			}
			o.ClusterName = name
		}
	case TargetContext:
		if o.KubeContext == "" {
			name, err := ask(r, out, "Which kubeconfig context", "")
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("a context name is required to install into an existing cluster")
			}
			o.KubeContext = name
		}
	}

	if o.APINodePort == 0 {
		port, err := askInt(r, out, "Host port for the API", DefaultAPINodePort)
		if err != nil {
			return err
		}
		o.APINodePort = port
	}

	if o.Database == "" {
		answer, err := choose(r, out,
			"Database?",
			[]string{
				"bundle one with the install (fine for evaluation)",
				"point at a database I already run",
			}, 1)
		if err != nil {
			return err
		}
		if answer == 1 {
			o.Database = string(config.DatabaseInternal)
		} else {
			o.Database = string(config.DatabaseExternal)
			url, err := ask(r, out, "Connection string", "")
			if err != nil {
				return err
			}
			if strings.TrimSpace(url) == "" {
				return fmt.Errorf("an external database needs its connection string")
			}
			o.DatabaseURL = url
		}
	}

	// Only worth asking when there is something to decide: a key of this name
	// already exists, so the install either adopts that administrator or mints
	// another one under a different name.
	if !o.NewAdminKey {
		name := o.AdminKeyName
		if name == "" {
			name = DefaultAdminKeyName
		}
		if path, exists := keyExists(name); exists {
			answer, err := choose(r, out,
				fmt.Sprintf("An administrator key named %q is already here (%s).", name, path),
				[]string{
					"reuse it -- you can already sign in with this key",
					"generate a new one under a different name",
				}, 1)
			if err != nil {
				return err
			}
			if answer == 2 {
				o.NewAdminKey = true
				newName, err := ask(r, out, "Name for the new key", name+"-2")
				if err != nil {
					return err
				}
				o.AdminKeyName = newName
			}
		}
	}

	if o.Image == "" {
		image, err := ask(r, out, "Server image", defaultImage())
		if err != nil {
			return err
		}
		o.Image = image
	}

	fmt.Fprintln(out)
	return nil
}

// ask reads one line, returning def when the answer is empty.
func ask(r *bufio.Reader, out io.Writer, prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(out, "\n%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "\n%s: ", prompt)
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func askInt(r *bufio.Reader, out io.Writer, prompt string, def int) (int, error) {
	for {
		answer, err := ask(r, out, prompt, strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(answer))
		if convErr == nil {
			return n, nil
		}
		fmt.Fprintf(out, "  %q is not a number.\n", answer)
	}
}

// choose presents a numbered list and returns the 1-based selection.
func choose(r *bufio.Reader, out io.Writer, prompt string, options []string, def int) (int, error) {
	fmt.Fprintf(out, "\n%s\n", prompt)
	for i, opt := range options {
		fmt.Fprintf(out, "  %d) %s\n", i+1, opt)
	}
	for {
		answer, err := ask(r, out, "Choose", strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(answer))
		if convErr == nil && n >= 1 && n <= len(options) {
			return n, nil
		}
		fmt.Fprintf(out, "  pick a number between 1 and %d.\n", len(options))
	}
}
