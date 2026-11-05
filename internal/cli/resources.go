package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
	"github.com/kontrolplane/koldstart/internal/state"
)

// checkResources refuses resource types cloud control cannot create, and properties the schema
// of their type does not allow, before anything is planned. Where the registry cannot be read, as
// on an emulator, the check is left to cloud control.
func (s *stage) checkResources(ctx context.Context) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(s.Resources)) {
		typ := s.Resources[name].Type
		schema, err := s.sess.Schemas.Schema(ctx, typ)
		if awserr.Is(err, "TypeNotFoundException") {
			errs = append(errs, fmt.Errorf("resources[%s]: %s is not a resource type: see https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-template-resource-type-ref.html", name, typ))
			continue
		}
		if err != nil {
			slog.Debug("not checking the resource type", "type", typ, "error", err)
			continue
		}
		if !schema.Provisionable {
			errs = append(errs, fmt.Errorf("resources[%s]: cloud control cannot create %s", name, typ))
			continue
		}
		if problems := schema.Check(s.Resources[name].Properties); len(problems) > 0 {
			errs = append(errs, propertyProblems(name, typ, problems))
		}
	}
	return errors.Join(errs...)
}

// propertyProblems lists what the schema of a resource's type does not allow, one a line.
func propertyProblems(name, typ string, problems []cloudcontrol.Problem) error {
	var b strings.Builder
	fmt.Fprintf(&b, "resources[%s] does not fit the schema of %s:", name, typ)
	for _, p := range problems {
		if p.Path == "" {
			fmt.Fprintf(&b, "\n  %s", p.Message)
			continue
		}
		fmt.Fprintf(&b, "\n  properties.%s", p)
	}
	return errors.New(b.String())
}

func importCommand(name string, opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <resource> <identifier>",
		Short: "take over a resource that exists already",
		Long: `import takes over an existing resource as one of the resources in the configuration, by the
identifier cloud control knows it by: a table's name, a queue's url, a bucket's name. It reads the
resource into the state and changes nothing in aws; the next deploy brings it in line with the
configuration. An imported resource is retained, so remove leaves it in place, unless its
deletionPolicy says delete.`,
		Example: fmt.Sprintf(`  %[1]s import orders orders-prod --stage prod
  %[1]s import jobs https://sqs.eu-west-1.amazonaws.com/123456789012/jobs`, name),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resource, identifier := args[0], args[1]
			s, err := openStage(ctx, opts, true)
			if err != nil {
				return err
			}
			r, ok := s.Resources[resource]
			if !ok {
				return fmt.Errorf("no resource %q in %s; it has %s: add it to resources first", resource, s.Path, list(slices.Sorted(maps.Keys(s.Resources))))
			}
			unlock, err := s.lockFor(cmd, "import "+resource)
			if err != nil {
				return err
			}
			defer unlock()
			id := desired.ResourceID(resource)
			if existing := s.state.Resources[id]; existing != nil {
				return fmt.Errorf("%s is managed already, as %s: there is nothing to import", resource, existing.PhysicalID)
			}
			out, err := s.sess.CloudControl.GetResource(ctx, &awscloudcontrol.GetResourceInput{TypeName: &r.Type, Identifier: &identifier})
			if err != nil {
				return fmt.Errorf("reading the %s %s: %w", r.Type, identifier, err)
			}
			var live map[string]any
			if err := json.Unmarshal([]byte(aws.ToString(out.ResourceDescription.Properties)), &live); err != nil {
				return fmt.Errorf("reading the %s %s: %w", r.Type, identifier, err)
			}
			entry, err := importedEntry(r.Type, identifier, r.DeletionPolicy, r.Properties, live)
			if err != nil {
				return err
			}
			store := engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())
			if err := store.Update(ctx, func(st *state.State) {
				st.Resources[id] = entry
				delete(st.Retained, id)
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "imported %s as %s\nrun %s plan to see what the next deploy changes to match the configuration\n", identifier, resource, name)
			return err
		},
	}
	return cmd
}

// importedEntry is the state of an imported resource. Its inputs are its live properties of the
// keys the configuration sets, so the first deploy patches what differs and nothing else, and its
// hash is empty, so that deploy always looks.
func importedEntry(typ, identifier, policy string, configured, live map[string]any) (*state.Resource, error) {
	props := map[string]any{}
	for k := range configured {
		if v, ok := live[k]; ok {
			props[k] = v
		}
	}
	inputs, err := json.Marshal(cloudcontrol.Inputs{Type: typ, Properties: props})
	if err != nil {
		return nil, err
	}
	if policy == "" {
		policy = engine.PolicyRetain
	}
	if strings.EqualFold(policy, engine.PolicyDelete) {
		policy = engine.PolicyDelete
	}
	return &state.Resource{
		Type:           typ,
		PhysicalID:     identifier,
		Inputs:         inputs,
		Attributes:     cloudcontrol.Attributes(identifier, live),
		DeletionPolicy: policy,
		Imported:       true,
	}, nil
}
