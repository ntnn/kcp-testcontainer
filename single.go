// Package kcp provides a testcontainers-go module running a kcp server.
package kcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
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
	instance
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

	kubeconfigBytes, err := kubeconfig(ctx, container)
	if err != nil {
		return nil, fmt.Errorf("error getting kubeconfig: %w", err)
	}

	si := &SingleInstance{
		Container: container,
		instance: instance{
			kubeconfig: kubeconfigBytes,
		},
	}

	return si, nil
}
