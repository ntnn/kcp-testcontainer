# kcp-testcontainer

[testcontainers-go](https://golang.testcontainers.org/) module running a
[kcp](https://kcp.io) server for tests.

## Usage

```go
import kcp "github.com/ntnn/kcp-testcontainer"

kcpc, err := kcp.Run(ctx, "ghcr.io/kcp-dev/kcp:latest")

// create workspaces (parents included)
err = kcpc.CreateWorkspace(ctx, "root:my:workspace")
path, err := kcpc.CreateWorkspaceGenerateName(ctx, "root", "test-")

// access a workspace
cfg, err := kcpc.RESTConfig(ctx, "root:my:workspace")
cl, err := kcpc.Client(ctx, "root:my:workspace", client.Options{})
```
