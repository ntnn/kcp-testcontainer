package kcp

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
)

func TestPKI(t *testing.T) {
	t.Parallel()

	serving, err := newCA("serving-ca")
	require.NoError(t, err)
	client, err := newCA("client-ca")
	require.NoError(t, err)

	servingPool := x509.NewCertPool()
	require.True(t, servingPool.AppendCertsFromPEM(serving.pem))
	clientPool := x509.NewCertPool()
	require.True(t, clientPool.AppendCertsFromPEM(client.pem))

	verify := func(kp keyPair, pool *x509.CertPool, dnsName string) {
		t.Helper()
		_, err := tls.X509KeyPair(kp.cert, kp.key)
		require.NoError(t, err)
		block, _ := pem.Decode(kp.cert)
		require.NotNil(t, block)
		cert, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		_, err = cert.Verify(x509.VerifyOptions{
			Roots:     pool,
			DNSName:   dnsName,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		})
		require.NoError(t, err)
	}

	server, err := serving.serverCert("kcp-0", "localhost")
	require.NoError(t, err)
	verify(server, servingPool, "kcp-0")
	verify(server, servingPool, "localhost")

	admin, err := client.clientCert("kcp-admin", "system:kcp:admin")
	require.NoError(t, err)
	verify(admin, clientPool, "")

	sa, err := selfSignedKeyPair("service-account")
	require.NoError(t, err)
	_, err = tls.X509KeyPair(sa.cert, sa.key)
	require.NoError(t, err)

	raw, err := kubeconfigBytes("https://localhost:12345/clusters/root", serving.pem, admin)
	require.NoError(t, err)
	cfg, err := clientcmd.Load(raw)
	require.NoError(t, err)
	require.Equal(t, "https://localhost:12345/clusters/root",
		cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster].Server)
}
