# Benchmarks

Starts N control planes with envtest and kcp-testcontainer and compares
their resource consumption and ttr.

For envtest N envtest environments are started.
For kcp-testcontainer one kcp testcontainer is started and N workspaces are created.

`main.go` orchestrates the benchmark, writes the results to a csv and
outputs a summary. `run.sh` wraps it, providing `KUBEBUILDER_ASSETS`
via setup-envtest when unset.

```sh
benchmarks/run.sh -count 5 -runs 3
```
