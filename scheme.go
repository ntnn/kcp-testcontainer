package kcp

import (
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

// Small helper only used internally.
func tenancyScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(tenancyv1alpha1.AddToScheme(s))
	utilruntime.Must(corev1alpha1.AddToScheme(s))
	return s
}
