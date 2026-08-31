// Boots a kcp testcontainer, creates COUNT workspaces, prints boot time and container id, holds until SIGTERM/SIGINT.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	kcp "github.com/ntnn/kcp-testcontainer"
)

func main() {
	img := os.Getenv("KCP_IMAGE")
	if img == "" {
		img = "ghcr.io/kcp-dev/kcp:latest"
	}
	count := 1
	if c := os.Getenv("COUNT"); c != "" {
		var err error
		if count, err = strconv.Atoi(c); err != nil {
			fmt.Fprintln(os.Stderr, "invalid COUNT:", err)
			os.Exit(1)
		}
	}
	ctx := context.Background()

	start := time.Now()
	container, err := kcp.Single(ctx, img)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kcp run:", err)
		os.Exit(1)
	}
	for i := range count {
		if err := container.CreateWorkspace(ctx, fmt.Sprintf("root:bench-%d", i)); err != nil {
			fmt.Fprintln(os.Stderr, "create workspace:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("READY boot_ms=%d container_id=%s\n", time.Since(start).Milliseconds(), container.GetContainerID())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	if err := container.Terminate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "kcp terminate:", err)
		os.Exit(1)
	}
}
