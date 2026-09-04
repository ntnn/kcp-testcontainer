package kcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/testcontainers/testcontainers-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pollTimeout  = time.Minute
	tickInterval = 100 * time.Millisecond
)

// helper to extract the admin kubeconfig.
func kubeconfig(ctx context.Context, container testcontainers.Container) ([]byte, error) {
	reader, err := container.CopyFileFromContainer(ctx, kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("copying admin kubeconfig from container: %w", err)
	}
	defer reader.Close()

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading admin kubeconfig: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving container host: %w", err)
	}
	mapped, err := container.MappedPort(ctx, port)
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

type instance struct {
	kubeconfig []byte
}

// Kubeconfig returns the admin kubeconfig.
// The current context is the root workspace.
func (in *instance) Kubeconfig(_ context.Context) ([]byte, error) {
	if in.kubeconfig == nil {
		return nil, errors.New("instance not started: kubeconfig not set")
	}
	return in.kubeconfig, nil
}

// RESTConfig returns an admin rest.Config for the workspace at path.
func (in *instance) RESTConfig(ctx context.Context, path string) (*rest.Config, error) {
	kubeconfig, err := in.Kubeconfig(ctx)
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
func (in *instance) Client(ctx context.Context, path string, opts client.Options) (client.Client, error) {
	config, err := in.RESTConfig(ctx, path)
	if err != nil {
		return nil, err
	}

	cl, err := client.New(config, opts)
	if err != nil {
		return nil, fmt.Errorf("creating client for %q: %w", path, err)
	}
	return cl, nil
}

// WorkspaceOption mutates a workspace before creation.
type WorkspaceOption func(ws *tenancyv1alpha1.Workspace)

// WithLocation sets the location of the workspace.
func WithLocation(w tenancyv1alpha1.WorkspaceLocation) WorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Spec.Location = &w
	}
}

// WithShard schedules the workspace on the given shard.
func WithShard(name string) WorkspaceOption {
	return WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"name": name,
		},
	}})
}

// CreateWorkspace creates the workspace at path, including missing parents.
// A path segment ending in "-" is used as a GenerateName prefix.
// The full path with generated names is returned.
// Options apply only to the last workspace in the path.
func (in *instance) CreateWorkspace(ctx context.Context, path string, opts ...WorkspaceOption) (string, error) {
	segments := strings.Split(path, ":")
	if segments[0] != "root" {
		return "", fmt.Errorf("workspace path %q must start with root", path)
	}
	if len(segments) < 2 {
		return "", fmt.Errorf("workspace path %q names no workspace to create", path)
	}

	for i := 1; i < len(segments); i++ {
		parent := strings.Join(segments[:i], ":")
		var segmentOpts []WorkspaceOption
		if i == len(segments)-1 {
			segmentOpts = opts
		}
		name, err := in.createChildWorkspace(ctx, parent, segments[i], segmentOpts...)
		if err != nil {
			return "", fmt.Errorf("creating workspace %q in %q: %w", segments[i], parent, err)
		}
		segments[i] = name
	}
	return strings.Join(segments, ":"), nil
}

func (in *instance) createChildWorkspace(ctx context.Context, parent, name string, opts ...WorkspaceOption) (string, error) {
	cl, err := in.Client(ctx, parent, client.Options{Scheme: tenancyScheme()})
	if err != nil {
		return "", err
	}

	workspace := &tenancyv1alpha1.Workspace{}
	if strings.HasSuffix(name, "-") {
		workspace.SetGenerateName(name)
	} else {
		workspace.SetName(name)
	}
	for _, opt := range opts {
		opt(workspace)
	}
	if err := cl.Create(ctx, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating workspace object: %w", err)
	}

	if err := waitReady(ctx, cl, workspace.Name); err != nil {
		return "", err
	}
	return workspace.Name, nil
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
