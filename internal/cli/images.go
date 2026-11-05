package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
)

// untaggedLifecycle expires the images of a repository that lost their tag, a day after. Tagged
// images are kept: lambda stops a function whose image is gone.
const untaggedLifecycle = `{"rules":[{"rulePriority":1,"description":"untagged images, a day after","selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":1},"action":{"type":"expire"}}]}`

// registry is the ecr registry of the session and the password docker logs in to it with, from
// an authorization token.
type registry struct {
	host     string
	endpoint string
	password string
}

// imageRegistry reads the session's ecr registry, once.
func (s *stage) imageRegistry(ctx context.Context) (registry, error) {
	if s.registry.host != "" {
		return s.registry, nil
	}
	out, err := ecr.NewFromConfig(s.sess.cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return registry{}, fmt.Errorf("reading the ecr registry: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return registry{}, fmt.Errorf("reading the ecr registry: no authorization data")
	}
	data := out.AuthorizationData[0]
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(data.AuthorizationToken))
	if err != nil {
		return registry{}, fmt.Errorf("reading the ecr token: %w", err)
	}
	_, password, _ := strings.Cut(string(decoded), ":")
	endpoint := aws.ToString(data.ProxyEndpoint)
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	s.registry = registry{host: host, endpoint: endpoint, password: password}
	return s.registry, nil
}

// pushImage builds a function's image and pushes it to the app's repository, which it creates
// when there is none, unless an image of the same tag, built from the same, is there already.
func (s *stage) pushImage(ctx context.Context, name string, fn config.Function, arc build.Archive) error {
	reg, err := s.imageRegistry(ctx)
	if err != nil {
		return err
	}
	client := ecr.NewFromConfig(s.sess.cfg)
	repo := build.ImageRepository(s.App)
	if err := ensureRepository(ctx, client, repo, s.App); err != nil {
		return err
	}
	tag := build.ImageTag(s.Stage, name, arc.SHA256)
	images, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: &repo, ImageIds: []ecrtypes.ImageIdentifier{{ImageTag: &tag}}})
	switch {
	case err == nil && len(images.ImageDetails) > 0:
		return nil
	case err != nil && !awserr.Is(err, "ImageNotFoundException"):
		return fmt.Errorf("looking for the image %s:%s: %w", repo, tag, err)
	}
	if err := dockerLogin(ctx, reg); err != nil {
		return err
	}
	b := build.Builder{Root: s.Root, Stage: s.Stage}
	return b.BuildImage(ctx, fn, build.LambdaTarget(fn.Architecture), reg.host+"/"+repo+":"+tag, true)
}

// ensureRepository creates the app's repository when there is none, scanning what is pushed and
// expiring images that lost their tag.
func ensureRepository(ctx context.Context, client *ecr.Client, repo, app string) error {
	_, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{repo}})
	if err == nil {
		return nil
	}
	if !awserr.Is(err, "RepositoryNotFoundException") {
		return fmt.Errorf("looking for the repository %s: %w", repo, err)
	}
	_, err = client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:             &repo,
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: true},
		Tags:                       []ecrtypes.Tag{{Key: aws.String("koldstart:app"), Value: &app}},
	})
	if err != nil && !awserr.Is(err, "RepositoryAlreadyExistsException") {
		return fmt.Errorf("creating the repository %s: %w", repo, err)
	}
	_, err = client.PutLifecyclePolicy(ctx, &ecr.PutLifecyclePolicyInput{RepositoryName: &repo, LifecyclePolicyText: aws.String(untaggedLifecycle)})
	if err != nil {
		return fmt.Errorf("setting the lifecycle of %s: %w", repo, err)
	}
	return nil
}

// dockerLogin logs docker in to the registry with the token's password, given on stdin.
//
//nolint:gosec // runs docker login to the account's own registry
func dockerLogin(ctx context.Context, reg registry) error {
	cmd := exec.CommandContext(ctx, "docker", "login", "--username", "AWS", "--password-stdin", reg.endpoint)
	cmd.Stdin = strings.NewReader(reg.password)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker login to %s: %w\n%s", reg.host, err, strings.TrimSpace(out.String()))
	}
	return nil
}
