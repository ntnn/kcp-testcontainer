# kcp-testcontainer

[testcontainers-go](https://golang.testcontainers.org/) module running a [kcp](https://kcp.io) server for tests.

Two modes are supported. `Single` and `Sharded`.

`Single` is a single kcp shard and a great way to get cheap control planes.

`Sharded` is a full setup with a front-proxy, two shards and a cache-server and a great way to test kcp-native operators.

## Usage

Import this module:

```go
import kcptc "github.com/ntnn/kcp-testcontainer"
```

<!--
```go ci
package kcp_test

import  (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kcptc "github.com/ntnn/kcp-testcontainer"
)

var _ = func(ctx context.Context) {
```
-->

Create an instance, either `Single`, `Sharded`, `SingleOnce` or `ShardedOnce` and create workspaces:

```go ci

single, err := kcptc.Single(ctx, kcptc.DefaultImage)

// create workspaces (parents included)
path, err := single.CreateWorkspace(ctx, "root:my:workspace")
// create workspace with generated name (parents included)
path, err = single.CreateWorkspace(ctx, "root:test-")

// rest.Config for a workspace
cfg, err := single.RESTConfig(ctx, "root:my:workspace")
// controller-runtime client for a workspace
cl, err := single.Client(ctx, "root:my:workspace", client.Options{})
```

<!--
```go ci
    // prevent `go vet` from erroring on unused variabled
    _ = path
    _ = err
    _ = cfg
    _ = cl
}
```
-->

## Why not envtest?

Starting multiple envtest environments is also an option, but each is
its own process and consumer memory and cpu. This might not be a big
problem on large machines but on smaller laptops resource consumption is
a concern.

E.g. ten control planes (envtest environments or kcp workspaces) cost
~380 MiB in memory in kcp and 3.1GiB with envtest.

Here are benchmarks comparing envtest with kcp-testcontainer deploying ten control planes over five passes:

```text
> ./benchmarks/run.sh -count 10 -runs 5
env          min ms   avg ms   max ms  avg mem MiB
envtest       20310    21531    23568       3101.6
kcp           16295    18042    19105        379.8
```

On top of that kcp's workspaces are garbage collected, envtest has
disabled the GC.

However this is only useful if needing multiple control planes - if only
a few control planes are needed and memory isn't a concern it is still
faster to use envtest directly.

```text
> ./benchmarks/run.sh -count 1 -runs 5
env          min ms   avg ms   max ms  avg mem MiB
envtest        1987     2249     2585        341.1
kcp           15897    16254    16802        256.7
```

kcp takes longer because it does the heavy lifting for multiple control
planes during startup, but it pays off in memory consumption and when
needing multiple control planes.
