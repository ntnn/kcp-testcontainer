#!/usr/bin/env sh
# Wrapper: provide envtest binaries via setup-envtest, then run the benchmark.

die() { echo "$@" >&2; exit 1; }

cd "$(dirname "$0")" || die "cd failed"

if test -z "$KUBEBUILDER_ASSETS"; then
    KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21 use -p path)" \
        || die "setup-envtest failed"
    export KUBEBUILDER_ASSETS
fi

go run . "$@"
