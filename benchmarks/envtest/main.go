// Boots COUNT envtest environments, prints boot time, holds until SIGTERM/SIGINT.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func main() {
	count := 1
	if c := os.Getenv("COUNT"); c != "" {
		var err error
		if count, err = strconv.Atoi(c); err != nil {
			fmt.Fprintln(os.Stderr, "invalid COUNT:", err)
			os.Exit(1)
		}
	}

	envs := make([]*envtest.Environment, count)
	start := time.Now()
	for i := range envs {
		envs[i] = &envtest.Environment{}
		if _, err := envs[i].Start(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest start:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("READY boot_ms=%d\n", time.Since(start).Milliseconds())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	for _, env := range envs {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}
}
