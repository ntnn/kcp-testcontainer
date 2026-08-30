// Compare boot time and memory of N envtest environments vs one kcp testcontainer with N workspaces.
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// settle before sampling memory so both apiservers are past warm-up
const settle = 5 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	count := flag.Int("count", 10, "control planes per run")
	runs := flag.Int("runs", 5, "runs per environment")
	img := flag.String("image", "ghcr.io/kcp-dev/kcp:latest", "kcp image")
	flag.Parse()

	resultPath := fmt.Sprintf("results-%s.csv", time.Now().Format("20060102-150405"))
	f, err := os.Create(resultPath)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"environment", "count", "boot_ms", "mem_kb"})

	type result struct{ bootMS, memKB int64 }
	results := map[string][]result{}

	record := func(env string, bootMS, memKB int64) {
		w.Write([]string{env, strconv.Itoa(*count), strconv.FormatInt(bootMS, 10), strconv.FormatInt(memKB, 10)})
		w.Flush()
		results[env] = append(results[env], result{bootMS, memKB})
		log.Printf("%s: %d control planes, boot %dms, rss %dkB", env, *count, bootMS, memKB)
	}

	for _, env := range []string{"envtest", "kcp"} {
		build := exec.Command("go", "build", "-o", "bin/"+env, "./"+env)
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			log.Fatalf("building %s: %v", env, err)
		}
	}

	for run := 1; run <= *runs; run++ {
		log.Printf("run %d/%d", run, *runs)
		for _, env := range []string{"envtest", "kcp"} {
			bootMS, memKB, err := bench(env, *count, *img)
			if err != nil {
				log.Fatalf("%s: %v", env, err)
			}
			record(env, bootMS, memKB)
		}
	}

	log.Print("results in ", resultPath)
	fmt.Printf("%-10s %8s %8s %8s %12s\n", "env", "min ms", "avg ms", "max ms", "avg mem MiB")
	for env, rs := range results {
		minB, maxB, sumBoot, sumMem := rs[0].bootMS, rs[0].bootMS, int64(0), int64(0)
		for _, r := range rs {
			sumBoot += r.bootMS
			sumMem += r.memKB
			if r.bootMS < minB {
				minB = r.bootMS
			}
			if r.bootMS > maxB {
				maxB = r.bootMS
			}
		}
		n := int64(len(rs))
		fmt.Printf("%-10s %8d %8d %8d %12.1f\n", env, minB, sumBoot/n, maxB, float64(sumMem)/float64(n)/1024)
	}
	return nil
}

// bench spawns ./<env>, waits for its READY line, samples memory, stops it.
func bench(env string, count int, img string) (bootMS, memKB int64, err error) {
	cmd := exec.Command("bin/" + env)
	cmd.Env = append(os.Environ(),
		"COUNT="+strconv.Itoa(count),
		"KCP_IMAGE="+img,
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	var line string
	for scanner.Scan() {
		// envtest: READY boot_ms=4681
		// kcp:     READY boot_ms=16495 container_id=3f2a9c...
		if strings.HasPrefix(scanner.Text(), "READY ") {
			line = scanner.Text()
			break
		}
	}
	if line == "" {
		return 0, 0, fmt.Errorf("no READY line")
	}

	fields := map[string]string{}
	// "READY boot_ms=16495 container_id=3f2a9c..."
	// -> "boot_ms=16495 container_id=3f2a9c..."
	// -> {"boot_ms": "16495", "container_id": "3f2a9c..."}
	for _, kv := range strings.Fields(strings.TrimPrefix(line, "READY ")) {
		k, v, _ := strings.Cut(kv, "=")
		fields[k] = v
	}
	bootMS, err = strconv.ParseInt(fields["boot_ms"], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing boot_ms: %w", err)
	}

	time.Sleep(settle)
	if cid := fields["container_id"]; cid != "" {
		memKB, err = containerMemKB(context.Background(), cid)
	} else {
		memKB, err = rssTreeKB(cmd.Process.Pid)
	}
	return bootMS, memKB, err
}

// rssTreeKB sums VmRSS of pid and all descendants.
func rssTreeKB(pid int) (int64, error) {
	children := map[int][]int{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, p := range procs {
		id, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", p.Name(), "stat"))
		if err != nil {
			continue
		}
		// /proc/<pid>/stat: "1234 (some comm) S 987 ..."
		// comm may itself contain spaces and parentheses
		// look after the last `)`, " S 987 ..." -> fields[1] is ppid
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		ppid, _ := strconv.Atoi(fields[1])
		children[ppid] = append(children[ppid], id)
	}

	var total int64
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = append(queue[1:], children[p]...)
		total += vmRSSKB(p)
	}
	return total, nil
}

func vmRSSKB(pid int) int64 {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// /proc/<pid>/status line: "VmRSS:\t  354812 kB" -> 354812
		if after, ok := strings.CutPrefix(scanner.Text(), "VmRSS:"); ok {
			kb, _ := strconv.ParseInt(strings.Fields(after)[0], 10, 64)
			return kb
		}
	}
	return 0
}

// containerMemKB reads the container cgroup's memory usage via the docker API.
func containerMemKB(ctx context.Context, cid string) (int64, error) {
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return 0, err
	}
	defer cli.Close()

	stats, err := cli.ContainerStatsOneShot(ctx, cid)
	if err != nil {
		return 0, err
	}
	defer stats.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&s); err != nil {
		return 0, err
	}
	return int64(s.MemoryStats.Usage / 1024), nil
}
