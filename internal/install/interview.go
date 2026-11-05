package install

import (
	"bufio"
	"context"
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
	return interview(in, out, o, connectKubeContext)
}

// connectKubeContext reaches the cluster of a kubeconfig context with kubectl.
func connectKubeContext(kubeContext string) storageCluster {
	return kubeEnv{context: kubeContext}
}

// interview is Interview, reaching the cluster it is asked about through
// connect, once the context is known.
func interview(in io.Reader, out io.Writer, o *Options, connect func(kubeContext string) storageCluster) error {
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
		// Unset, the server derives the registry's scheme, host and port from the
		// client's request, which is right for a kind cluster. Only a cluster of
		// the operator's own can put the registry where a request can't tell.
		// Blank is the common answer, so it is the default, and it keeps a URL a
		// previous run recorded.
		if o.OCIURL == "" {
			fmt.Fprintln(out, "\nWhere do clients, such as Argo CD in another cluster, reach the OCI registry?")
			fmt.Fprintln(out, "Leave it blank unless it is behind a load balancer that terminates TLS, or on a name other than oci.<API host>.")
			for {
				url, err := ask(r, out, "Registry URL, like https://oci.example.com", "")
				if err != nil {
					return err
				}
				if err := config.ValidateOCIURL(url); err != nil {
					fmt.Fprintf(out, "  %v\n", err)
					continue
				}
				o.OCIURL = strings.TrimSpace(url)
				break
			}
		}
		if err := askNamespace(r, out, o); err != nil {
			return err
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

	if err := askStorageClass(r, out, o, connect); err != nil {
		return err
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

	// Offered, not demanded. The default is whatever the registry says is
	// newest, which is not known yet and is what almost everyone wants; taking
	// it leaves Image empty so the install resolves it.
	if o.Image == "" {
		const newest = "newest released"
		image, err := ask(r, out, "Server image", newest)
		if err != nil {
			return err
		}
		if image != newest {
			o.Image = image
		}
	}

	fmt.Fprintln(out)
	return nil
}

// askNamespace asks whether the install creates its namespace and, if it does,
// for the labels and annotations the cluster's admission policies require. Only
// a cluster of the operator's own has such policies. Answering with the
// defaults leaves the settings unset, so a re-run keeps what was recorded.
func askNamespace(r *bufio.Reader, out io.Writer, o *Options) error {
	if o.CreateNamespace == nil && !o.NamespaceLabelsSet && !o.NamespaceAnnotationsSet {
		namespace := o.Namespace
		if namespace == "" {
			namespace = DefaultNamespace
		}
		answer, err := choose(r, out,
			fmt.Sprintf("The namespace %q?", namespace),
			[]string{
				"create it (or keep what a previous install chose)",
				"it exists already, created by whoever runs the cluster; leave it as it is",
			}, 1)
		if err != nil {
			return err
		}
		if answer == 2 {
			create := false
			o.CreateNamespace = &create
			return nil
		}
	}
	if !o.createsNamespace() {
		return nil
	}
	if !o.NamespaceLabelsSet {
		fmt.Fprintln(out, "\nDo the cluster's admission policies require labels on a namespace? Enter each as key=value, and a blank line to finish.")
		labels, err := askKeyValues(r, out, "Namespace label", validateLabelValue)
		if err != nil {
			return err
		}
		if len(labels) > 0 {
			o.NamespaceLabels, o.NamespaceLabelsSet = labels, true
		}
	}
	if !o.NamespaceAnnotationsSet {
		annotations, err := askKeyValues(r, out, "Namespace annotation", nil)
		if err != nil {
			return err
		}
		if len(annotations) > 0 {
			o.NamespaceAnnotations, o.NamespaceAnnotationsSet = annotations, true
		}
	}
	return nil
}

// askKeyValues asks for key=value pairs until a blank answer.
func askKeyValues(r *bufio.Reader, out io.Writer, prompt string, validateValue func(string) error) (map[string]string, error) {
	got := map[string]string{}
	for {
		answer, err := ask(r, out, prompt+" (blank to finish)", "")
		if err != nil {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return got, nil
		}
		parsed, err := parseKeyValues(strings.ToLower(prompt), []string{answer}, validateValue)
		if err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		for k, v := range parsed {
			got[k] = v
		}
	}
}

// askStorageClass asks which StorageClass the bundled database's volume comes
// from, when installing into a cluster that has no default one. Anywhere else
// the default class is the answer, and asking would be a keystroke tax: kind
// has one, and so does a cluster that names one. Nor on a re-run whose previous
// render named one, which the install keeps.
func askStorageClass(r *bufio.Reader, out io.Writer, o *Options, connect func(kubeContext string) storageCluster) error {
	if o.Target != TargetContext || o.StorageClass != "" ||
		(o.Database != "" && o.Database != string(config.DatabaseInternal)) {
		return nil
	}
	outDir := o.OutDir
	if outDir == "" {
		outDir, _ = defaultOutDir(o.instanceName())
	}
	if recorded, _, _ := renderedDatabaseClaim(outDir); recorded != "" {
		return nil
	}
	classes, err := connect(o.KubeContext).storageClasses(context.Background())
	if err != nil {
		// Not fatal: the install itself says what is wrong with the context.
		fmt.Fprintf(out, "\nCould not list the StorageClasses of %q, so not asking which the database uses.\n", o.KubeContext)
		return nil
	}
	if hasDefaultClass(classes) {
		return nil
	}

	fmt.Fprintf(out, "\nThe cluster %q has no default StorageClass, so the bundled database's volume\n", o.KubeContext)
	fmt.Fprintln(out, "needs one named, or its claim stays Pending and the database never starts.")
	if len(classes) == 0 {
		fmt.Fprintln(out, "It has no StorageClasses at all. Ask whoever runs it which to use, or install with")
		fmt.Fprintln(out, "--database=external --database-url <url> to bring your own database.")
		for {
			name, err := ask(r, out, "StorageClass", "")
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("the bundled database needs a StorageClass on %q: pass --storage-class, or --database=external", o.KubeContext)
			}
			if err := config.ValidateStorageClass(name); err != nil {
				fmt.Fprintf(out, "  %v\n", err)
				continue
			}
			o.StorageClass = strings.TrimSpace(name)
			return nil
		}
	}

	options := make([]string, 0, len(classes))
	for _, c := range classes {
		options = append(options, fmt.Sprintf("%s (%s)", c.Name, c.Provisioner))
	}
	answer, err := choose(r, out, "Which StorageClass should the database's volume come from?", options, 1)
	if err != nil {
		return err
	}
	o.StorageClass = classes[answer-1].Name
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
