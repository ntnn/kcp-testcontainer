package kcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/docker/go-connections/nat"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	"k8s.io/apimachinery/pkg/util/wait"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Network aliases of the sharded topology components.
const (
	AliasCache      = "cache"
	AliasShard0     = "root" // TODO(ntnn): rename to kcp-0 once root shard is no longer a thing
	AliasShard1     = "kcp-1"
	AliasFrontProxy = "front-proxy"
)

const (
	cachePort      = "8012/tcp"
	shardPort      = "6444/tcp"
	frontProxyPort = "6443/tcp"
)

const (
	pkiDir = "/etc/kcp-pki"

	caPath = pkiDir + "/ca.crt"

	serviceAccountCertPath = pkiDir + "/service-account.crt"
	serviceAccountKeyPath  = pkiDir + "/service-account.key"

	servingCertPath = pkiDir + "/apiserver.crt"
	servingKeyPath  = pkiDir + "/apiserver.key"

	shardClientCertPath = pkiDir + "/shard-client.crt"
	shardClientKeyPath  = pkiDir + "/shard-client.key"

	requestHeaderClientCertPath = pkiDir + "/requestheader.crt"
	requestHeaderClientKeyPath  = pkiDir + "/requestheader.key"

	cacheKubeconfigPath                       = pkiDir + "/cache.kubeconfig"
	rootShardKubeconfigPath                   = pkiDir + "/root-shard.kubeconfig"
	rootKubeconfigPath                        = pkiDir + "/root.kubeconfig"
	shardsKubeconfigPath                      = pkiDir + "/shards.kubeconfig"
	logicalClusterAdminKubeconfigPath         = pkiDir + "/logical-cluster-admin.kubeconfig"
	externalLogicalClusterAdminKubeconfigPath = pkiDir + "/external-logical-cluster-admin.kubeconfig"

	mappingPath = pkiDir + "/mapping.yaml"
)

type shardedConfig struct {
	customizers map[string][]testcontainers.ContainerCustomizer
}

func withCustomizers(alias string, opts []testcontainers.ContainerCustomizer) ShardedOption {
	return func(cfg *shardedConfig) error {
		cfg.customizers[alias] = append(cfg.customizers[alias], opts...)
		return nil
	}
}

// ShardedOption configures the kcp components.
type ShardedOption func(*shardedConfig) error

// WithCacheCustomizers applies customizers to the cache-server container.
func WithCacheCustomizers(opts ...testcontainers.ContainerCustomizer) ShardedOption {
	return withCustomizers(AliasCache, opts)
}

// WithShardCustomizers applies customizers to the shard container request.
func WithShardCustomizers(shard string, opts ...testcontainers.ContainerCustomizer) ShardedOption {
	return withCustomizers(shard, opts)
}

// WithFrontProxyCustomizers applies customizers to the front-proxy container request.
func WithFrontProxyCustomizers(opts ...testcontainers.ContainerCustomizer) ShardedOption {
	return withCustomizers(AliasFrontProxy, opts)
}

// ShardedInstance is a full kcp instance with front-proxy, two shards and a cache-server.
type ShardedInstance struct {
	instance

	FrontProxy testcontainers.Container
	Shard0     testcontainers.Container
	Shard1     testcontainers.Container
	Cache      testcontainers.Container

	network *testcontainers.DockerNetwork

	ca             *ca
	serviceAccount keyPair
	kcpAdmin       keyPair
	shardAdmin     keyPair
}

// Sharded starts a sharded kcp instance: cache-server, root shard, shard-1
// and front-proxy, all from img on a dedicated network.
func Sharded(ctx context.Context, img string, opts ...ShardedOption) (*ShardedInstance, error) {
	if img == "" {
		img = DefaultImage
	}

	cfg := &shardedConfig{customizers: map[string][]testcontainers.ContainerCustomizer{}}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	kcp := &ShardedInstance{}
	if err := kcp.start(ctx, img, cfg); err != nil {
		// tear down whatever came up; startup error takes precedence.
		_ = kcp.Terminate(context.WithoutCancel(ctx))
		return nil, err
	}
	return kcp, nil
}

func (in *ShardedInstance) start(ctx context.Context, img string, cfg *shardedConfig) error {
	network, err := tcnetwork.New(ctx)
	if err != nil {
		return fmt.Errorf("creating network: %w", err)
	}
	in.network = network

	if err := in.initPKI(); err != nil {
		return err
	}

	startContainer := func(alias string, build func() (testcontainers.ContainerRequest, error)) (testcontainers.Container, error) {
		req, err := build()
		if err != nil {
			return nil, err
		}
		req.Image = img
		req.Networks = []string{network.Name}
		req.NetworkAliases = map[string][]string{network.Name: {alias}}

		genericReq := testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		}
		for _, opt := range cfg.customizers[alias] {
			if err := opt.Customize(&genericReq); err != nil {
				return nil, fmt.Errorf("customizing %s container request: %w", alias, err)
			}
		}

		container, err := testcontainers.GenericContainer(ctx, genericReq)
		if err != nil {
			return container, fmt.Errorf("starting %s container: %w", alias, err)
		}
		return container, nil
	}

	in.Cache, err = startContainer(AliasCache, in.cacheRequest)
	if err != nil {
		return err
	}

	in.Shard0, err = startContainer(AliasShard0, func() (testcontainers.ContainerRequest, error) { return in.shardRequest(AliasShard0) })
	if err != nil {
		return err
	}

	in.Shard1, err = startContainer(AliasShard1, func() (testcontainers.ContainerRequest, error) { return in.shardRequest(AliasShard1) })
	if err != nil {
		return err
	}

	in.FrontProxy, err = startContainer(AliasFrontProxy, in.frontProxyRequest)
	if err != nil {
		return err
	}

	host, err := in.FrontProxy.Host(ctx)
	if err != nil {
		return fmt.Errorf("resolving container host: %w", err)
	}
	mapped, err := in.FrontProxy.MappedPort(ctx, frontProxyPort)
	if err != nil {
		return fmt.Errorf("resolving mapped port: %w", err)
	}
	in.instance.kubeconfig, err = kubeconfigBytes(
		"https://"+net.JoinHostPort(host, mapped.Port())+"/clusters/root",
		in.ca.pem,
		in.kcpAdmin,
	)
	if err != nil {
		return err
	}

	return in.waitForShards(ctx)
}

func (in *ShardedInstance) initPKI() error {
	var err error
	if in.ca, err = newCA("kcp-ca"); err != nil {
		return err
	}
	if in.serviceAccount, err = selfSignedKeyPair("kcp-service-account-signing-ca"); err != nil {
		return err
	}
	if in.kcpAdmin, err = in.ca.clientCert("kcp-admin", "system:kcp:admin"); err != nil {
		return err
	}
	if in.shardAdmin, err = in.ca.clientCert("shard-admin", kuser.SystemPrivilegedGroup); err != nil {
		return err
	}
	return nil
}

// ExternalURL rewrites an in-network URL (e.g. a virtual workspace URL
// https://kcp-1:6444/services/...) to the host-mapped endpoint.
func (in *ShardedInstance) ExternalURL(ctx context.Context, inNetwork string) (string, error) {
	u, err := url.Parse(inNetwork)
	if err != nil {
		return "", fmt.Errorf("parsing URL %q: %w", inNetwork, err)
	}

	var container testcontainers.Container
	var port nat.Port
	switch u.Hostname() {
	case AliasShard0:
		container, port = in.Shard0, shardPort
	case AliasShard1:
		container, port = in.Shard1, shardPort
	default:
		return "", fmt.Errorf("no container for host %q", u.Hostname())
	}

	host, err := container.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving container host: %w", err)
	}
	mapped, err := container.MappedPort(ctx, port)
	if err != nil {
		return "", fmt.Errorf("resolving mapped port for %q: %w", u.Hostname(), err)
	}

	u.Host = net.JoinHostPort(host, mapped.Port())
	return u.String(), nil
}

func (in *ShardedInstance) waitForShards(ctx context.Context) error {
	cl, err := in.Client(ctx, "root", client.Options{Scheme: tenancyScheme()})
	if err != nil {
		return err
	}

	if err := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			shards := &corev1alpha1.ShardList{}
			if err := cl.List(ctx, shards); err != nil {
				return false, nil //nolint:nilerr // front-proxy may still 503 while shards register
			}
			return len(shards.Items) >= 2, nil
		},
	); err != nil {
		return fmt.Errorf("waiting for both shards to register: %w", err)
	}
	return nil
}

// Terminate tears down all containers and the network.
func (in *ShardedInstance) Terminate(ctx context.Context, opts ...testcontainers.TerminateOption) error {
	var errs []error

	if in.FrontProxy != nil {
		if err := in.FrontProxy.Terminate(ctx, opts...); err != nil {
			errs = append(errs, fmt.Errorf("error terminating %s: %w", AliasFrontProxy, err))
		}
	}

	if in.Shard1 != nil {
		if err := in.Shard1.Terminate(ctx, opts...); err != nil {
			errs = append(errs, fmt.Errorf("error terminating %s: %w", AliasShard1, err))
		}
	}

	if in.Shard0 != nil {
		if err := in.Shard0.Terminate(ctx, opts...); err != nil {
			errs = append(errs, fmt.Errorf("error terminating %s: %w", AliasShard0, err))
		}
	}

	if in.Cache != nil {
		if err := in.Cache.Terminate(ctx, opts...); err != nil {
			errs = append(errs, fmt.Errorf("error terminating %s: %w", AliasCache, err))
		}
	}

	if in.network != nil {
		if err := in.network.Remove(ctx); err != nil {
			errs = append(errs, fmt.Errorf("removing network: %w", err))
		}
	}

	return errors.Join(errs...)
}

func file(path string, content []byte) testcontainers.ContainerFile {
	return testcontainers.ContainerFile{
		Reader:            bytes.NewReader(content),
		ContainerFilePath: path,
		// image runs as 65532 but the files are mounted with root
		FileMode: 0o644,
	}
}

func (in *ShardedInstance) waitReadyz(port nat.Port) tcwait.Strategy {
	cert, err := tls.X509KeyPair(in.kcpAdmin.cert, in.kcpAdmin.key)
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // readiness probe only
	if err == nil {
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tcwait.ForHTTP("/readyz").
		WithPort(port).
		WithTLS(true, tlsCfg).
		WithStartupTimeout(2 * time.Minute)
}

func (in *ShardedInstance) cacheRequest() (testcontainers.ContainerRequest, error) {
	serving, err := in.ca.serverCert(AliasCache, "localhost")
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}

	return testcontainers.ContainerRequest{
		Entrypoint: []string{"/cache-server"},
		Cmd: []string{
			"--root-directory=" + rootDirectory,
			"--bind-address=0.0.0.0",
			"--secure-port=8012",
			"--client-ca-file=" + caPath,
			"--tls-cert-file=" + servingCertPath,
			"--tls-private-key-file=" + servingKeyPath,
		},
		Files: []testcontainers.ContainerFile{
			file(caPath, in.ca.pem),
			file(servingCertPath, serving.cert),
			file(servingKeyPath, serving.key),
		},
		ExposedPorts: []string{cachePort},
		WaitingFor:   in.waitReadyz(cachePort),
	}, nil
}

func (in *ShardedInstance) shardRequest(alias string) (testcontainers.ContainerRequest, error) {
	serving, err := in.ca.serverCert(alias, "localhost", externalHostname)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	shardClient, err := in.ca.clientCert("kcp-shard-"+alias, kuser.SystemPrivilegedGroup)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	cacheClient, err := in.ca.clientCert("cache-client", kuser.SystemPrivilegedGroup)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	logicalClusterAdmin, err := in.ca.clientCert("logical-cluster-admin", "system:kcp:logical-cluster-admin")
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	externalLogicalClusterAdmin, err := in.ca.clientCert("external-logical-cluster-admin", "system:kcp:external-logical-cluster-admin")
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}

	cacheKubeconfig, err := kubeconfigBytes("https://"+AliasCache+":8012", in.ca.pem, cacheClient)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	logicalClusterAdminKubeconfig, err := kubeconfigBytes("https://"+AliasShard0+":6444", in.ca.pem, logicalClusterAdmin)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	externalLogicalClusterAdminKubeconfig, err := kubeconfigBytes("https://"+AliasFrontProxy+":6443", in.ca.pem, externalLogicalClusterAdmin)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}

	cmd := []string{
		"start",
		"--shard-name=" + alias,
		"--root-directory=" + rootDirectory,
		"--bind-address=0.0.0.0",
		"--secure-port=6444",
		"--shard-base-url=https://" + alias + ":6444",
		"--shard-external-url=https://" + AliasFrontProxy + ":6443",
		"--external-hostname=" + AliasFrontProxy,
		"--client-ca-file=" + caPath,
		"--requestheader-client-ca-file=" + caPath,
		"--requestheader-allowed-names=kcp-front-proxy",
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		"--service-account-key-file=" + serviceAccountCertPath,
		"--service-account-private-key-file=" + serviceAccountKeyPath,
		"--service-account-signing-key-file=" + serviceAccountKeyPath,
		"--tls-cert-file=" + servingCertPath,
		"--tls-private-key-file=" + servingKeyPath,
		"--shard-client-cert-file=" + shardClientCertPath,
		"--shard-client-key-file=" + shardClientKeyPath,
		"--shard-virtual-workspace-ca-file=" + caPath,
		"--cache-kubeconfig=" + cacheKubeconfigPath,
		"--logical-cluster-admin-kubeconfig=" + logicalClusterAdminKubeconfigPath,
		"--external-logical-cluster-admin-kubeconfig=" + externalLogicalClusterAdminKubeconfigPath,
	}
	files := []testcontainers.ContainerFile{
		file(caPath, in.ca.pem),
		file(serviceAccountCertPath, in.serviceAccount.cert),
		file(serviceAccountKeyPath, in.serviceAccount.key),
		file(servingCertPath, serving.cert),
		file(servingKeyPath, serving.key),
		file(shardClientCertPath, shardClient.cert),
		file(shardClientKeyPath, shardClient.key),
		file(cacheKubeconfigPath, cacheKubeconfig),
		file(logicalClusterAdminKubeconfigPath, logicalClusterAdminKubeconfig),
		file(externalLogicalClusterAdminKubeconfigPath, externalLogicalClusterAdminKubeconfig),
	}

	if alias != AliasShard0 {
		rootShardKubeconfig, err := kubeconfigBytes("https://"+AliasShard0+":6444", in.ca.pem, in.shardAdmin)
		if err != nil {
			return testcontainers.ContainerRequest{}, err
		}
		cmd = append(cmd,
			"--root-shard-kubeconfig-file="+rootShardKubeconfigPath,
		)
		files = append(files, file(rootShardKubeconfigPath, rootShardKubeconfig))
	}

	return testcontainers.ContainerRequest{
		Entrypoint:   []string{"/kcp"},
		Cmd:          cmd,
		Files:        files,
		ExposedPorts: []string{shardPort},
		WaitingFor:   in.waitReadyz(shardPort),
	}, nil
}

func (in *ShardedInstance) frontProxyRequest() (testcontainers.ContainerRequest, error) {
	serving, err := in.ca.serverCert(AliasFrontProxy, "localhost", externalHostname)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	requestHeaderClient, err := in.ca.clientCert("kcp-front-proxy")
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}

	rootKubeconfig, err := kubeconfigBytes("https://"+AliasShard0+":6444", in.ca.pem, in.shardAdmin)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}
	// credentials only; shard URLs come from Shard objects.
	shardsKubeconfig, err := kubeconfigBytes("", in.ca.pem, in.shardAdmin)
	if err != nil {
		return testcontainers.ContainerRequest{}, err
	}

	mapping := fmt.Sprintf(`- path: /clusters/
  backend: https://%s:6444
  backend_server_ca: %s
  proxy_client_cert: %s
  proxy_client_key: %s
`, AliasShard0, caPath, requestHeaderClientCertPath, requestHeaderClientKeyPath)

	return testcontainers.ContainerRequest{
		Entrypoint: []string{"/kcp-front-proxy"},
		Cmd: []string{
			"--root-directory=" + rootDirectory,
			"--secure-port=6443",
			"--mapping-file=" + mappingPath,
			"--root-kubeconfig=" + rootKubeconfigPath,
			"--shards-kubeconfig=" + shardsKubeconfigPath,
			"--client-ca-file=" + caPath,
			"--tls-cert-file=" + servingCertPath,
			"--tls-private-key-file=" + servingKeyPath,
		},
		Files: []testcontainers.ContainerFile{
			file(caPath, in.ca.pem),
			file(servingCertPath, serving.cert),
			file(servingKeyPath, serving.key),
			file(requestHeaderClientCertPath, requestHeaderClient.cert),
			file(requestHeaderClientKeyPath, requestHeaderClient.key),
			file(rootKubeconfigPath, rootKubeconfig),
			file(shardsKubeconfigPath, shardsKubeconfig),
			file(mappingPath, []byte(mapping)),
		},
		ExposedPorts: []string{frontProxyPort},
		WaitingFor:   in.waitReadyz(frontProxyPort),
	}, nil
}
