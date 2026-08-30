# kcp-testcontainer

[testcontainers-go](https://golang.testcontainers.org/) module running a
[kcp](https://kcp.io) server for tests.

This only provide a _single_ kcp instance, as in a single kcp root
shard. There is no front-proxy, no sharding, no external cache server.

As such this is only a cheap way to get multiple control planes to test components
that touch multiple control planes.

To build components against actual kcp infrastructure, such as operators
handling APIExports and -Bindings, use [envtest.Sharded](https://pkg.go.dev/github.com/kcp-dev/multicluster-provider@main/envtest#Sharded) instead.

## Usage

```go
import kcp "github.com/ntnn/kcp-testcontainer"

kcpc, err := kcp.Run(ctx, "ghcr.io/kcp-dev/kcp:latest")

// create workspaces (parents included)
err = kcpc.CreateWorkspace(ctx, "root:my:workspace")
// create workspace with generated names (parents included)
path, err := kcpc.CreateWorkspaceGenerateName(ctx, "root", "test-")

// rest.Config for a workspace
cfg, err := kcpc.RESTConfig(ctx, "root:my:workspace")
// controller-runtime client for a workspace
cl, err := kcpc.Client(ctx, "root:my:workspace", client.Options{})
```

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
