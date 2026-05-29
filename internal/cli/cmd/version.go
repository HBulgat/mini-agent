package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCmd implements `mini-agent version`. We print the four fields
// the Makefile actually injects (version / commit / build time / Go
// runtime) on a single multi-line block so it's easy to paste into a bug
// report.
//
// `version` opts out of config loading via skipConfigAnnotation so it
// works on a freshly-installed system that has no config.yaml yet — and
// so it never fails because of an unrelated config typo.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print build information",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfigAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "mini-agent %s\n", buildVersion)
			fmt.Fprintf(out, "  commit:    %s\n", buildCommit)
			fmt.Fprintf(out, "  built:     %s\n", buildTime)
			fmt.Fprintf(out, "  go:        %s %s/%s\n",
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
