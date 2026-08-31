// Package kcp provides a testcontainers-go module running a kcp server.
package kcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	port           = "6443/tcp"
	rootDirectory  = "/.kcp"
	kubeconfigPath = rootDirectory + "/admin.kubeconfig"
	// externalHostname must be a SAN of the generated serving cert and match
	// the host tests connect through; localhost covers the usual port mapping.
	externalHostname = "localhost"
)

// DefaultImage is the image kcp-testcontainer uses when an empty string is passed.
const DefaultImage = "ghcr.io/kcp-dev/kcp:v0.32.3"

// SingleInstance is a running a single kcp root shard.
type SingleInstance struct {
	testcontainers.Container
}

// Small helper only used internally to constructo ctrl-runtime clients
// with the kcp tenancy API in the scheme to create workspaces.
func tenancyScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(tenancyv1alpha1.AddToScheme(s))
	return s
}

// Single starts a single root-shard kcp container.
func Single(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*SingleInstance, error) {
	if img == "" {
		img = DefaultImage
	}
	req := testcontainers.ContainerRequest{
		Image:        img,
		ExposedPorts: []string{port},
		Cmd: []string{
			"start",
			"--root-directory=" + rootDirectory,
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
	return &SingleInstance{Container: container}, nil
}

// Kubeconfig returns the admin kubeconfig.
// The current context is the root workspace.
func (kcp *SingleInstance) Kubeconfig(ctx context.Context) ([]byte, error) {
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

	// the admin kubeconfig written in the container contains the
	// container IP instead of the external hostname.
	config, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing admin kubeconfig: %w", err)
	}
	cluster, ok := config.Clusters[config.Contexts[config.CurrentContext].Cluster]
	if !ok {
		return nil, fmt.Errorf("kubeconfig has no cluster for current context %q", config.CurrentContext)
	}
	server, err := url.Parse(cluster.Server)
	if err != nil {
		return nil, fmt.Errorf("parsing server URL %q: %w", cluster.Server, err)
	}

	rewritten := strings.ReplaceAll(string(raw),
		"https://"+server.Host,
		"https://"+net.JoinHostPort(host, mapped.Port()))
	return []byte(rewritten), nil
}

// RESTConfig returns an admin rest.Config for the workspace at path.
func (kcp *SingleInstance) RESTConfig(ctx context.Context, path string) (*rest.Config, error) {
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
func (kcp *SingleInstance) Client(ctx context.Context, path string, opts client.Options) (client.Client, error) {
	config, err := kcp.RESTConfig(ctx, path)
	if err != nil {
		return nil, err
	}

	cl, err := client.New(config, opts)
	if err != nil {
		return nil, fmt.Errorf("creating client for %q: %w", path, err)
	}
	return cl, nil
}

// CreateWorkspace creates the workspace at path, including missing parents.
func (kcp *SingleInstance) CreateWorkspace(ctx context.Context, path string) error {
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

// CreateWorkspaceGenerateName creates a workspace in parent using prefix as GenerateName.
// Missing parents are created.
// The full path is returned.
func (kcp *SingleInstance) CreateWorkspaceGenerateName(ctx context.Context, parent, prefix string) (string, error) {
	if strings.Contains(parent, ":") {
		if err := kcp.CreateWorkspace(ctx, parent); err != nil {
			return "", err
		}
	}

	cl, err := kcp.Client(ctx, parent, client.Options{Scheme: tenancyScheme()})
	if err != nil {
		return "", err
	}

	workspace := &tenancyv1alpha1.Workspace{}
	workspace.SetGenerateName(prefix)
	if err := cl.Create(ctx, workspace); err != nil {
		return "", fmt.Errorf("creating workspace with generate name %q in %q: %w", prefix, parent, err)
	}

	if err := waitReady(ctx, cl, workspace.Name); err != nil {
		return "", fmt.Errorf("workspace %q in %q: %w", workspace.Name, parent, err)
	}
	return parent + ":" + workspace.Name, nil
}

func (kcp *SingleInstance) createChildWorkspace(ctx context.Context, parent, name string) error {
	cl, err := kcp.Client(ctx, parent, client.Options{Scheme: tenancyScheme()})
	if err != nil {
		return err
	}

	workspace := &tenancyv1alpha1.Workspace{}
	workspace.SetName(name)
	if err := cl.Create(ctx, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating workspace object: %w", err)
	}

	return waitReady(ctx, cl, name)
}

func waitReady(ctx context.Context, cl client.Client, name string) error {
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
