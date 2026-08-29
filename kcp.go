// Package kcp provides a testcontainers-go module running a kcp server.
package kcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	port           = "6443/tcp"
	kubeconfigPath = "/data/.kcp/admin.kubeconfig"
	// externalHostname must be a SAN of the generated serving cert and match
	// the host tests connect through; localhost covers the usual port mapping.
	externalHostname = "localhost"
)

// Container is a running kcp server.
type Container struct {
	testcontainers.Container
}

// Run starts a kcp container from img (e.g. "ghcr.io/kcp-dev/kcp:latest").
func Run(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        img,
		ExposedPorts: []string{port},
		Cmd: []string{
			"start",
			"--bind-address=0.0.0.0",
			"--external-hostname=" + externalHostname,
		},
		WaitingFor: tcwait.ForHTTP("/readyz").
			WithPort(port).
			WithTLS(true, &tls.Config{InsecureSkipVerify: true}).
			WithStartupTimeout(2 * time.Minute),
	}

	genericReq := testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	}
	for _, opt := range opts {
		if err := opt.Customize(&genericReq); err != nil {
			return nil, fmt.Errorf("customizing container request: %w", err)
		}
	}

	container, err := testcontainers.GenericContainer(ctx, genericReq)
	if err != nil {
		return nil, fmt.Errorf("starting kcp container: %w", err)
	}
	return &Container{Container: container}, nil
}

// Kubeconfig returns the admin kubeconfig.
// The current context is the root workspace.
func (kcp *Container) Kubeconfig(ctx context.Context) ([]byte, error) {
	reader, err := kcp.CopyFileFromContainer(ctx, kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("copying admin kubeconfig from container: %w", err)
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading admin kubeconfig: %w", err)
	}

	host, err := kcp.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving container host: %w", err)
	}
	mapped, err := kcp.MappedPort(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("resolving mapped port: %w", err)
	}

	rewritten := strings.ReplaceAll(string(raw),
		"https://"+net.JoinHostPort(externalHostname, "6443"),
		"https://"+net.JoinHostPort(host, mapped.Port()))
	return []byte(rewritten), nil
}

// RESTConfig returns an admin rest.Config for the workspace at path.
func (kcp *Container) RESTConfig(ctx context.Context, path string) (*rest.Config, error) {
	kubeconfig, err := kcp.Kubeconfig(ctx)
	if err != nil {
		return nil, err
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing admin kubeconfig: %w", err)
	}

	// current context points at /clusters/root; retarget to the given path.
	config.Host = strings.TrimSuffix(config.Host, "/clusters/root") + "/clusters/" + path
	return config, nil
}

// Client returns a controller-runtime client for the workspace at path.
func (kcp *Container) Client(ctx context.Context, path string, opts client.Options) (client.Client, error) {
	config, err := kcp.RESTConfig(ctx, path)
	if err != nil {
		return nil, err
	}

	if opts.Scheme == nil {
		opts.Scheme = runtime.NewScheme()
	}
	if err := tenancyv1alpha1.AddToScheme(opts.Scheme); err != nil {
		return nil, fmt.Errorf("adding tenancy types to scheme: %w", err)
	}

	cl, err := client.New(config, opts)
	if err != nil {
		return nil, fmt.Errorf("creating client for %q: %w", path, err)
	}
	return cl, nil
}

// CreateWorkspace creates the workspace at path, including missing parents.
func (kcp *Container) CreateWorkspace(ctx context.Context, path string) error {
	segments := strings.Split(path, ":")
	if segments[0] != "root" {
		return fmt.Errorf("workspace path %q must start with root", path)
	}
	if len(segments) < 2 {
		return fmt.Errorf("workspace path %q names no workspace to create", path)
	}

	for i := 1; i < len(segments); i++ {
		parent := strings.Join(segments[:i], ":")
		if err := kcp.createChildWorkspace(ctx, parent, segments[i]); err != nil {
			return fmt.Errorf("creating workspace %q in %q: %w", segments[i], parent, err)
		}
	}
	return nil
}

func (kcp *Container) createChildWorkspace(ctx context.Context, parent, name string) error {
	cl, err := kcp.Client(ctx, parent, client.Options{})
	if err != nil {
		return err
	}

	workspace := &tenancyv1alpha1.Workspace{}
	workspace.SetName(name)
	if err := cl.Create(ctx, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating workspace object: %w", err)
	}

	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, time.Minute, true,
		func(ctx context.Context) (bool, error) {
			current := &tenancyv1alpha1.Workspace{}
			if err := cl.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
				return false, fmt.Errorf("getting workspace: %w", err)
			}
			return current.Status.Phase == corev1alpha1.LogicalClusterPhaseReady, nil
		},
	); err != nil {
		return fmt.Errorf("waiting for workspace to become ready: %w", err)
	}
	return nil
}
