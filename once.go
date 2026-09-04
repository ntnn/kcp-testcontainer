package kcp

import (
	"context"
	"sync"

	"github.com/testcontainers/testcontainers-go"
)

var (
	singleOnce     sync.Once
	errSingleOnce  error
	singleOnceInst *SingleInstance
)

// SingleOnce is like [Single] but always returns the same [SingleInstance].
//
// This is once per process. To deploy a single instance overall use [testcontainers.WithReuseByName].
func SingleOnce(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*SingleInstance, error) {
	singleOnce.Do(func() {
		singleOnceInst, errSingleOnce = Single(ctx, img, opts...)
	})
	return singleOnceInst, errSingleOnce
}

var (
	shardedOnce     sync.Once
	errShardedOnce  error
	shardedOnceInst *ShardedInstance
)

// ShardedOnce is like [Sharded] but always returns the same [ShardedInstance].
//
// This is once per process. Deploying across multiple processes is not
// recommended since the kcp instance relies on pki, which for
// kcp-testcontainer is held in memory.
//
// For a persistent sharded test setup use envtest.Sharded from kcp's multicluster-provider:
// https://pkg.go.dev/github.com/kcp-dev/multicluster-provider@main/envtest#Sharded
func ShardedOnce(ctx context.Context, img string, opts ...ShardedOption) (*ShardedInstance, error) {
	shardedOnce.Do(func() {
		shardedOnceInst, errShardedOnce = Sharded(ctx, img, opts...)
	})
	return shardedOnceInst, errShardedOnce
}
