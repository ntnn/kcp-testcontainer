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

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
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
		WaitingFor: wait.ForHTTP("/readyz").
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
