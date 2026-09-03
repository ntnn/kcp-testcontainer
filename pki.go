package kcp

import (
	"fmt"
	"time"

	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const certLifetime = 24 * time.Hour * 365

type keyPair struct {
	cert []byte
	key  []byte
}

// ca is a thin wrapper around library-go's crypto.CA issuing in-memory PEM
// certs; nothing is written to host disk.
type ca struct {
	ca *crypto.CA
	// pem is the CA certificate for trust bundles.
	pem []byte
}

func newCA(name string) (*ca, error) {
	cfg, err := crypto.MakeSelfSignedCAConfig(name, 365)
	if err != nil {
		return nil, fmt.Errorf("creating CA %q: %w", name, err)
	}
	certPEM, _, err := cfg.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("encoding CA %q: %w", name, err)
	}
	return &ca{
		ca: &crypto.CA{
			Config:          cfg,
			SerialGenerator: &crypto.RandomSerialGenerator{},
		},
		pem: certPEM,
	}, nil
}

// serverCert issues a serving cert with the given hostnames/IPs as SANs.
func (c *ca) serverCert(hostnames ...string) (keyPair, error) {
	cfg, err := c.ca.MakeServerCertForDuration(sets.New(hostnames...), certLifetime)
	if err != nil {
		return keyPair{}, fmt.Errorf("creating serving cert for %v: %w", hostnames, err)
	}
	return toKeyPair(cfg)
}

// clientCert issues a client cert for the given user and groups.
func (c *ca) clientCert(name string, groups ...string) (keyPair, error) {
	cfg, err := c.ca.MakeClientCertificateForDuration(
		&kuser.DefaultInfo{
			Name:   name,
			Groups: groups,
		},
		certLifetime,
	)
	if err != nil {
		return keyPair{}, fmt.Errorf("creating client cert %q: %w", name, err)
	}
	return toKeyPair(cfg)
}

// selfSignedKeyPair returns a standalone signing keypair, e.g. for service
// account tokens.
func selfSignedKeyPair(name string) (keyPair, error) {
	cfg, err := crypto.MakeSelfSignedCAConfig(name, 365)
	if err != nil {
		return keyPair{}, fmt.Errorf("creating self-signed keypair %q: %w", name, err)
	}
	return toKeyPair(cfg)
}

func toKeyPair(cfg *crypto.TLSCertificateConfig) (keyPair, error) {
	cert, key, err := cfg.GetPEMBytes()
	if err != nil {
		return keyPair{}, fmt.Errorf("encoding cert: %w", err)
	}
	return keyPair{cert: cert, key: key}, nil
}

// kubeconfigBytes builds a single-context kubeconfig with inline cert data.
func kubeconfigBytes(server string, caPEM []byte, client keyPair) ([]byte, error) {
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {
				Server:                   server,
				CertificateAuthorityData: caPEM,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {
				ClientCertificateData: client.cert,
				ClientKeyData:         client.key,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {
				Cluster:  "cluster",
				AuthInfo: "user",
			},
			// hardcoded contexts kcp uses
			"base": {
				Cluster:  "cluster",
				AuthInfo: "user",
			},
			"shard-base": {
				Cluster:  "cluster",
				AuthInfo: "user",
			},
		},
		CurrentContext: "default",
	}
	raw, err := clientcmd.Write(cfg)
	if err != nil {
		return nil, fmt.Errorf("serializing kubeconfig for %q: %w", server, err)
	}
	return raw, nil
}
