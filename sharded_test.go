package kcp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSharded(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	instance, err := Sharded(ctx, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, instance.Terminate(context.WithoutCancel(ctx)))
	})

	kubeconfig, err := instance.Kubeconfig(ctx)
	require.NoError(t, err)

	config, err := clientcmd.Load(kubeconfig)
	require.NoError(t, err)
	cluster := config.Clusters[config.Contexts[config.CurrentContext].Cluster]
	require.NotNil(t, cluster)
	require.Contains(t, cluster.Server, "/clusters/root")
	require.NotContains(t, cluster.Server, ":6443", "server URL must be rewritten to the mapped port")

	t.Run("root workspace reachable with admin client cert", func(t *testing.T) {
		t.Parallel()

		user := config.AuthInfos[config.Contexts[config.CurrentContext].AuthInfo]
		require.NotNil(t, user)
		cert, err := tls.X509KeyPair(user.ClientCertificateData, user.ClientKeyData)
		require.NoError(t, err)

		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
					Certificates:       []tls.Certificate{cert},
				},
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			cluster.Server+"/apis/tenancy.kcp.io/v1alpha1/workspaces", http.NoBody)
		require.NoError(t, err)

		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		require.Contains(t, string(body), "WorkspaceList", "expected a WorkspaceList response")
	})

	t.Run("both shards registered", func(t *testing.T) {
		t.Parallel()

		cl, err := instance.Client(ctx, "root", client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)

		shards := &corev1alpha1.ShardList{}
		require.NoError(t, cl.List(ctx, shards))
		require.Len(t, shards.Items, 2)
	})

	t.Run("create workspace scheduled to "+AliasShard1, func(t *testing.T) {
		t.Parallel()

		_, err := instance.CreateWorkspace(ctx, "root:pinned", WithShard(AliasShard1))
		require.NoError(t, err)

		cl, err := instance.Client(ctx, "root", client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)

		workspace := &tenancyv1alpha1.Workspace{}
		require.NoError(t, cl.Get(ctx, client.ObjectKey{Name: "pinned"}, workspace))
		hash := sha256.Sum224([]byte(AliasShard1))
		require.Equal(t, new(big.Int).SetBytes(hash[:]).Text(36)[:8],
			workspace.Annotations["internal.tenancy.kcp.io/shard"],
			"workspace must be scheduled to %s", AliasShard1)

		pinned, err := instance.Client(ctx, "root:pinned", client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)
		require.NoError(t, pinned.List(ctx, &tenancyv1alpha1.WorkspaceList{}))
	})

	t.Run("create nested workspace and get client by path", func(t *testing.T) {
		t.Parallel()

		_, err := instance.CreateWorkspace(ctx, "root:my-org:team")
		require.NoError(t, err)
		_, err = instance.CreateWorkspace(ctx, "root:my-org:team")
		require.NoError(t, err)

		cl, err := instance.Client(ctx, "root:my-org", client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)

		workspaces := &tenancyv1alpha1.WorkspaceList{}
		require.NoError(t, cl.List(ctx, workspaces))
		require.Len(t, workspaces.Items, 1)
		require.Equal(t, "team", workspaces.Items[0].Name)
	})

	t.Run("create workspace with generate name", func(t *testing.T) {
		t.Parallel()

		path, err := instance.CreateWorkspace(ctx, "root:gen-")
		require.NoError(t, err)
		require.Regexp(t, `^root:gen-.+`, path)

		cl, err := instance.Client(ctx, path, client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)
		require.NoError(t, cl.List(ctx, &tenancyv1alpha1.WorkspaceList{}), "generated workspace must be usable by returned path")
	})

	t.Run("create workspace with generate name with parents", func(t *testing.T) {
		t.Parallel()

		path, err := instance.CreateWorkspace(ctx, "root:with:parents:gen-")
		require.NoError(t, err)
		require.Regexp(t, `^root:with:parents:gen-.+`, path)

		cl, err := instance.Client(ctx, path, client.Options{Scheme: tenancyScheme()})
		require.NoError(t, err)
		require.NoError(t, cl.List(ctx, &tenancyv1alpha1.WorkspaceList{}), "generated workspace must be usable by returned path")
	})
}

const sheriffSchema = `{
	"description": "Sheriff is part of the wild west",
	"type": "object",
	"properties": {
		"spec": {
			"type": "object",
			"properties": {"intent": {"type": "string"}}
		}
	}
}`

func TestShardedVirtualWorkspace(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	t.Log("Starting sharded kcp instance")
	instance, err := Sharded(ctx, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, instance.Terminate(context.WithoutCancel(ctx)))
	})

	t.Log("Creating provider workspace")
	_, err = instance.CreateWorkspace(ctx, "root:provider", WithShard(AliasShard0))
	require.NoError(t, err)
	providerClient, err := instance.Client(ctx, "root:provider", client.Options{Scheme: tenancyScheme()})
	require.NoError(t, err)

	t.Log("Creating consumer workspace")
	_, err = instance.CreateWorkspace(ctx, "root:consumer", WithShard(AliasShard1))
	require.NoError(t, err)
	consumerClient, err := instance.Client(ctx, "root:consumer", client.Options{Scheme: tenancyScheme()})
	require.NoError(t, err)

	t.Log("Creating APIResourceSchema in root:provider")
	sheriffs := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: "v1.sheriffs.wildwest.dev"},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "wildwest.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name:    "v1alpha1",
				Served:  true,
				Storage: true,
				Schema:  runtime.RawExtension{Raw: []byte(sheriffSchema)},
			}},
		},
	}
	require.NoError(t, providerClient.Create(ctx, sheriffs))

	t.Log("Creating APIExport in root:provider")
	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest.dev"},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"v1.sheriffs.wildwest.dev"},
		},
	}
	require.NoError(t, providerClient.Create(ctx, export))

	t.Log("Creating APIBinding in root:consumer")
	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest.dev"},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: "root:provider",
					Name: "wildwest.dev",
				},
			},
		},
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, consumerClient.Create(ctx, binding.DeepCopy()))
	}, time.Minute, 500*time.Millisecond, "APIBinding must be created")

	t.Log("Waiting for APIBinding to become bound")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		current := &apisv1alpha1.APIBinding{}
		require.NoError(c, consumerClient.Get(ctx, client.ObjectKey{Name: "wildwest.dev"}, current))
		assert.Equal(c, apisv1alpha1.APIBindingPhaseBound, current.Status.Phase)
	}, time.Minute, 500*time.Millisecond, "APIBinding must become bound")

	t.Log("Creating Sheriff in root:consumer")
	sheriff := &unstructured.Unstructured{}
	sheriff.SetGroupVersionKind(schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "Sheriff"})
	sheriff.SetName("woody")
	require.NoError(t, consumerClient.Create(ctx, sheriff))

	vwURL := ""
	t.Log("Waiting for the APIExportEndpointSlice to publish the virtual workspace URL")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		slice := &apisv1alpha1.APIExportEndpointSlice{}
		require.NoError(c, providerClient.Get(ctx, client.ObjectKey{Name: "wildwest.dev"}, slice))
		require.NotEmpty(c, slice.Status.APIExportEndpoints)
		vwURL = slice.Status.APIExportEndpoints[0].URL
	}, time.Minute, 500*time.Millisecond, "APIExportEndpointSlice must publish a virtual workspace URL")

	external, err := instance.ExternalURL(ctx, vwURL)
	require.NoError(t, err)
	t.Logf("Virtual workspace URL %s mapped to %s", vwURL, external)

	kubeconfig, err := instance.Kubeconfig(ctx)
	require.NoError(t, err)
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	require.NoError(t, err)

	config.Host = external + "/clusters/*"
	config.TLSClientConfig.Insecure = true
	config.TLSClientConfig.CAData = nil

	vwClient, err := client.New(config, client.Options{Scheme: tenancyScheme()})
	require.NoError(t, err)

	t.Log("Listing sheriffs through the virtual workspace")
	sheriffsList := &unstructured.UnstructuredList{}
	sheriffsList.SetGroupVersionKind(schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "SheriffList"})
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.NoError(c, vwClient.List(ctx, sheriffsList))
		assert.Len(c, sheriffsList.Items, 1)
	}, time.Minute, 500*time.Millisecond, "sheriff must be visible through the virtual workspace")
	require.Equal(t, "woody", sheriffsList.Items[0].GetName())
}
