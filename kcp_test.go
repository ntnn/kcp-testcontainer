package kcp

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	tc "github.com/testcontainers/testcontainers-go"
)

func TestRun(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	container, err := Run(ctx, "ghcr.io/kcp-dev/kcp:latest")
	require.NoError(t, err)
	tc.CleanupContainer(t, container)

	kubeconfig, err := container.Kubeconfig(ctx)
	require.NoError(t, err)

	var config struct {
		Clusters []struct {
			Cluster struct {
				Server string `yaml:"server"`
			} `yaml:"cluster"`
			Name string `yaml:"name"`
		} `yaml:"clusters"`
		Users []struct {
			User struct {
				Token string `yaml:"token"`
			} `yaml:"user"`
		} `yaml:"users"`
	}
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

		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			rootServer+"/apis/tenancy.kcp.io/v1alpha1/workspaces", http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+config.Users[0].User.Token)

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		require.Contains(t, string(body), "WorkspaceList", "expected a WorkspaceList response")
	})
}
