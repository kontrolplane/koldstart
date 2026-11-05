package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
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
		v, err := s.outputValue(st, s.Outputs[name])
		if err != nil {
			return nil, fmt.Errorf("outputs[%s]: %w", name, err)
		}
		out[name] = v
	}
	maps.Copy(out, s.urlOutputs(st))
	return out, nil
}

// outputValue resolves an output against the state: a string, a ref(), a join of them, or a value
// of the account.
func (s *stage) outputValue(st *state.State, v config.Value) (string, error) {
	switch {
	case v.Ref != nil:
		r := st.Resources[desired.NodeID(s.Project, v.Ref.Resource)]
		if r == nil {
			return "", fmt.Errorf("%s does not exist", v.Ref.Resource)
		}
		return r.Attributes[v.Ref.Attribute], nil
	case v.Join != nil:
		var b strings.Builder
		for _, part := range v.Join {
			p, err := s.outputValue(st, part)
			if err != nil {
				return "", err
			}
			b.WriteString(p)
		}
		return b.String(), nil
	case v.Pseudo == config.PseudoAccountID:
		return s.sess.Account, nil
	case v.Pseudo == config.PseudoPartition:
		return awsp.Partition(s.Region), nil
	case v.Pseudo == config.PseudoURLSuffix:
		if strings.HasPrefix(s.Region, "cn-") {
			return "amazonaws.com.cn", nil
		}
		return "amazonaws.com", nil
	case v.Secret != nil || v.Output != nil:
		return "", errors.New("an output is a string, a ref() or a join() of them")
	}
	return v.Literal, nil
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
	if r := st.Resources[desired.WebSocketID]; r != nil {
		out["webSocketUrl"] = r.Attributes["ApiEndpoint"] + "/" + r.Attributes["Stage"]
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
