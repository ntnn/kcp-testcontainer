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
func ShardedOnce(ctx context.Context, img string, opts ...ShardedOption) (*ShardedInstance, error) {
	shardedOnce.Do(func() {
		shardedOnceInst, errShardedOnce = Sharded(ctx, img, opts...)
	})
	return shardedOnceInst, errShardedOnce
}
