package kcp

import (
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

// Small helper only used internally to constructo ctrl-runtime clients
// with the kcp tenancy API in the scheme to create workspaces.
func tenancyScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(tenancyv1alpha1.AddToScheme(s))
	return s
}
