package kcp

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/stretchr/testify/require"
	tc "github.com/testcontainers/testcontainers-go"
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type kubeConfig struct {
	Clusters []namedCluster `yaml:"clusters"`
	Users    []namedUser    `yaml:"users"`
}

type namedCluster struct {
	Cluster cluster `yaml:"cluster"`
	Name    string  `yaml:"name"`
}

type cluster struct {
	Server string `yaml:"server"`
}

type namedUser struct {
	User user `yaml:"user"`
}

type user struct {
	Token string `yaml:"token"`
}

func TestRun(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	container, err := Run(ctx, "ghcr.io/kcp-dev/kcp:latest")
	require.NoError(t, err)
	tc.CleanupContainer(t, container)

	kubeconfig, err := container.Kubeconfig(ctx)
	require.NoError(t, err)

	config := &kubeConfig{}
	require.NoError(t, yaml.Unmarshal(kubeconfig, &config))
	require.NotEmpty(t, config.Clusters)
	require.NotEmpty(t, config.Users)

	var rootServer string
	for _, cluster := range config.Clusters {
		if cluster.Name == "root" {
			rootServer = cluster.Cluster.Server
		}
	}
	require.NotEmpty(t, rootServer, "kubeconfig must contain a root cluster")
	require.NotContains(t, rootServer, ":6443", "server URL must be rewritten to the mapped port")

	t.Run("root workspace reachable with admin token", func(t *testing.T) {
		t.Parallel()

		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			rootServer+"/apis/tenancy.kcp.io/v1alpha1/workspaces", http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+config.Users[0].User.Token)

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		require.Contains(t, string(body), "WorkspaceList", "expected a WorkspaceList response")
	})

	t.Run("create nested workspace and get client by path", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, container.CreateWorkspace(ctx, "root:my-org:team"))
		require.NoError(t, container.CreateWorkspace(ctx, "root:my-org:team"))

		cl, err := container.Client(ctx, "root:my-org", client.Options{})
		require.NoError(t, err)

		workspaces := &tenancyv1alpha1.WorkspaceList{}
		require.NoError(t, cl.List(ctx, workspaces))
		require.Len(t, workspaces.Items, 1)
		require.Equal(t, "team", workspaces.Items[0].Name)
	})

	t.Run("create workspace with generate name", func(t *testing.T) {
		t.Parallel()

		path, err := container.CreateWorkspaceGenerateName(ctx, "root", "gen-")
		require.NoError(t, err)
		require.Regexp(t, `^root:gen-.+`, path)

		cl, err := container.Client(ctx, path, client.Options{})
		require.NoError(t, err)
		require.NoError(t, cl.List(ctx, &tenancyv1alpha1.WorkspaceList{}), "generated workspace must be usable by returned path")
	})
}
