package cli

import (
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/state"
)

func outputsCommand(name string, opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "outputs",
		Short:   "print the stage's outputs, such as the http api url",
		Example: fmt.Sprintf("  %[1]s outputs --stage prod", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStage(cmd.Context(), opts, false)
			if err != nil {
				return err
			}
			printOutputs(cmd.OutOrStdout(), s.state.Outputs)
			return nil
		},
	}
}

// outputs resolves the configuration's outputs against the state, and adds the urls.
func (s *stage) outputs(st *state.State) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(s.Outputs)) {
		v := s.Outputs[name]
		switch {
		case v.Ref != nil:
			r := st.Resources[desired.NodeID(s.Project, v.Ref.Resource)]
			if r == nil {
				return nil, fmt.Errorf("outputs[%s]: %s does not exist", name, v.Ref.Resource)
			}
			out[name] = r.Attributes[v.Ref.Attribute]
		case v.Secret != nil || v.Output != nil:
			return nil, fmt.Errorf("outputs[%s]: an output is a string or a ref()", name)
		default:
			out[name] = v.Literal
		}
	}
	maps.Copy(out, s.urlOutputs(st))
	return out, nil
}

// urlOutputs are the urls of the functions that have one, as <function>Url, and of the http
// api, as httpApiUrl. With a custom domain, httpDomainTarget is where its dns record points.
func (s *stage) urlOutputs(st *state.State) map[string]string {
	out := map[string]string{}
	for name := range s.Functions {
		if r := st.Resources[desired.URLID(name)]; r != nil {
			out[name+"Url"] = r.Attributes["FunctionUrl"]
		}
	}
	if r := st.Resources[desired.HTTPAPIID]; r != nil {
		out["httpApiUrl"] = r.Attributes["ApiEndpoint"]
	}
	if r := st.Resources[desired.DomainID]; r != nil {
		out["httpApiUrl"] = "https://" + r.PhysicalID
		out["httpDomainTarget"] = r.Attributes["RegionalDomainName"]
	}
	return out
}

func printOutputs(w io.Writer, outputs map[string]string) {
	if len(outputs) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\noutputs:")
	for _, k := range slices.Sorted(maps.Keys(outputs)) {
		_, _ = fmt.Fprintf(w, "  %s = %s\n", k, outputs[k])
	}
}
