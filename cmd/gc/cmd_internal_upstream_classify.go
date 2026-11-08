package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newInternalUpstreamClassifyCmd exposes the upstream-probe close gate's path
// split, so the city's upstream audit judges a landed patch by the rule the
// gate closed it under instead of a second copy that drifts. It prints
// {"kept": [...], "reverted": [...], "behavioral": [...]} for the given
// paths changed from --base to --head.
func newInternalUpstreamClassifyCmd(stdout, stderr io.Writer) *cobra.Command {
	var repoDir, base, head string
	cmd := &cobra.Command{
		Use:    "upstream-classify [path...]",
		Short:  "Split changed paths as the upstream-probe close gate does",
		Hidden: true,
		RunE: func(_ *cobra.Command, paths []string) error {
			// An empty --repo is git -C "", which reads the cwd's repository.
			if strings.TrimSpace(repoDir) == "" {
				fmt.Fprintln(stderr, "gc internal upstream-classify: --repo is required") //nolint:errcheck // best-effort stderr
				return errExit
			}
			// An unresolvable or empty rev would read every path as behavioral,
			// which looks like an answer rather than a typo.
			for _, f := range []struct{ name, rev string }{{"--base", base}, {"--head", head}} {
				if _, err := upstreamProbeGitOutput(repoDir, "rev-parse", "--verify", "--quiet", f.rev+"^{commit}"); err != nil {
					fmt.Fprintf(stderr, "gc internal upstream-classify: %s %q is not a commit in %s: %v\n", f.name, f.rev, repoDir, err) //nolint:errcheck // best-effort stderr
					return errExit
				}
			}
			kept, reverted, behavioral := upstreamProbeClassifyPaths(repoDir, base, head, paths)
			out, err := json.Marshal(struct {
				Kept       []string `json:"kept"`
				Reverted   []string `json:"reverted"`
				Behavioral []string `json:"behavioral"`
			}{nonNilStrings(kept), nonNilStrings(reverted), nonNilStrings(behavioral)})
			if err != nil {
				fmt.Fprintf(stderr, "gc internal upstream-classify: encoding result: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			fmt.Fprintln(stdout, string(out)) //nolint:errcheck // best-effort stdout
			return nil
		},
	}
	cmd.Flags().StringVar(&repoDir, "repo", "", "git repository to read")
	cmd.Flags().StringVar(&base, "base", "", "revision the paths changed from")
	cmd.Flags().StringVar(&head, "head", "", "revision the paths changed to; its //go:embed patterns decide docs")
	return cmd
}
