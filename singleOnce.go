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
