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
	var output string
	cmd := &cobra.Command{
		Use:   "outputs",
		Short: "print the stage's outputs, such as the http api url",
		Long: `outputs prints the outputs the last deploy of the stage recorded: those of the configuration,
httpApiUrl, webSocketUrl and a <function>Url for each function url. -o json prints them as one
json object of strings, its keys sorted, and {} when there are none:

  {
    "apiUrl": "https://abc123.lambda-url.eu-west-1.on.aws/",
    "httpApiUrl": "https://abc123.execute-api.eu-west-1.amazonaws.com"
  }`,
		Example: fmt.Sprintf(`  %[1]s outputs
  %[1]s outputs --stage prod
  %[1]s outputs -o json | jq -r .httpApiUrl`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(cmd, output); err != nil {
				return err
			}
			s, err := openStage(cmd.Context(), opts, false)
			if err != nil {
				return err
			}
			if s.etag == "" {
				return fmt.Errorf("%s/%s is not deployed: deploy it with %s deploy --stage %s", s.App, s.Stage, name, s.Stage)
			}
			if output == outputJSON {
				return writeOutputsJSON(cmd.OutOrStdout(), s.state.Outputs)
			}
			if len(s.state.Outputs) == 0 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s/%s has no outputs\n", s.App, s.Stage)
				return nil
			}
			writeOutputs(cmd.OutOrStdout(), "", s.state.Outputs)
			return nil
		},
	}
	outputFlag(cmd, &output)
	return cmd
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
			return "", fmt.Errorf("%s is not deployed: deploy the stage", v.Ref.Resource)
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

// camelCase joins the words of a name of dashes, as an output's name does: stripe-webhook is
// stripeWebhook.
func camelCase(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words[1:] {
		if w != "" {
			words[i+1] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, "")
}

// urlOutputs are the urls of the functions that have one, as <function>Url with the function's
// name in camel case, stripeWebhookUrl for stripe-webhook, and of the http api, as httpApiUrl. With a custom domain, httpDomainTarget is where its dns record points.
func (s *stage) urlOutputs(st *state.State) map[string]string {
	out := map[string]string{}
	for name := range s.Functions {
		if r := st.Resources[desired.URLID(name)]; r != nil {
			out[camelCase(name)+"Url"] = r.Attributes["FunctionUrl"]
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

// printOutputs writes the outputs under a heading, after a deploy.
func printOutputs(w io.Writer, outputs map[string]string) {
	if len(outputs) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\noutputs:")
	writeOutputs(w, "  ", outputs)
}

// writeOutputs writes the outputs one a line, sorted, with their values lined up.
func writeOutputs(w io.Writer, indent string, outputs map[string]string) {
	keys := slices.Sorted(maps.Keys(outputs))
	width := 0
	for _, k := range keys {
		width = max(width, len(k))
	}
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "%s%-*s = %s\n", indent, width, k, outputs[k])
	}
}

// writeOutputsJSON writes the outputs as one json object, {} when there are none.
func writeOutputsJSON(w io.Writer, outputs map[string]string) error {
	if outputs == nil {
		outputs = map[string]string{}
	}
	return writeJSON(w, outputs)
}
