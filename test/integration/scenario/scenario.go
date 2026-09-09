//go:build integration
// +build integration

package scenario

import (
	"context"
	kt "k8s.io/client-go/testing"
	mv1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"time"

	"github.com/docent-net/cluster-bare-autoscaler/pkg/config"
	"github.com/docent-net/cluster-bare-autoscaler/pkg/controller"
	"github.com/docent-net/cluster-bare-autoscaler/pkg/nodeops"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corefake "k8s.io/client-go/kubernetes/fake"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

// --- Minimal test doubles ----------------------------------------------------

type ShutdownRecorder struct{ Calls []string }

func (s *ShutdownRecorder) Shutdown(_ context.Context, node string) error {
	s.Calls = append(s.Calls, node)
	return nil
}

type PowerOnRecorder struct{ PoweredOn []string }

func (p *PowerOnRecorder) PowerOn(_ context.Context, node, _ string) error {
	p.PoweredOn = append(p.PoweredOn, node)
	return nil
}

// --- Node helpers ------------------------------------------------------------

func ManagedNode(name string, ready bool) *v1.Node {
	n := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"cba.dev/is-managed": "true",
			},
			Annotations: map[string]string{
				nodeops.AnnotationMACAuto: "00:11:22:33:44:55",
			},
		},
	}
	status := v1.ConditionFalse
	if ready {
		status = v1.ConditionTrue
	}
	n.Status.Conditions = []v1.NodeCondition{
		{Type: v1.NodeReady, Status: status},
	}
	return n
}

func PoweredOffSince(n *v1.Node, since time.Time) *v1.Node {
	if n.Annotations == nil {
		n.Annotations = map[string]string{}
	}
	n.Annotations[nodeops.AnnotationPoweredOff] = since.UTC().Format(time.RFC3339)
	n.Spec.Unschedulable = true
	return n
}

func MarkReady(n *v1.Node) *v1.Node {
	n.Spec.Unschedulable = false
	n.Status.Conditions = []v1.NodeCondition{
		{Type: v1.NodeReady, Status: v1.ConditionTrue},
	}
	return n
}

// --- Reconciler wiring -------------------------------------------------------

func NewFakeClient(nodes ...*v1.Node) *corefake.Clientset {
	objs := make([]runtime.Object, 0, len(nodes))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	return corefake.NewSimpleClientset(objs...)
}

func MinimalConfig() *config.Config {
	return &config.Config{
		DryRun:       false,
		MinNodes:     0,
		Cooldown:     0,                // we call controller methods directly; keep 0 to avoid gating
		BootCooldown: 10 * time.Minute, // protects the freshly booted node
		NodeLabels:   config.NodeLabelConfig{Managed: "cba.dev/is-managed", Disabled: "cba.dev/disabled"},
		NodeAnnotations: config.NodeAnnotationConfig{
			MAC: nodeops.AnnotationMACAuto,
		},
		// Rotation defaults – enable; let tests adjust as needed
		Rotation: config.RotationConfig{
			Enabled:               true,
			MaxPoweredOffDuration: 1 * time.Hour,
			ExemptLabel:           "",
		},
		// LoadAverage defaults – disabled; tests can enable and use dry-run overrides
		LoadAverageStrategy: config.LoadAverageStrategyConfig{
			Enabled:                    false,
			ExcludeFromAggregateLabels: map[string]string{},
		},
	}
}

func NewReconciler(cfg *config.Config, client *corefake.Clientset, sh *ShutdownRecorder, pwr *PowerOnRecorder) *controller.Reconciler {
	mfake := metricsfake.NewSimpleClientset()
	mfake.PrependReactor("list", "nodes", func(kt.Action) (bool, runtime.Object, error) {
		nodes, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return true, nil, err
		}
		result := &mv1.NodeMetricsList{}
		for _, n := range nodes.Items {
			result.Items = append(result.Items, mv1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: n.Name}, Usage: v1.ResourceList{}})
		}
		return true, result, nil
	})

	return controller.NewReconciler(
		cfg,
		client,
		mfake,
		// override controllers for the test
		func(r *controller.Reconciler) { r.Shutdowner = sh },
		func(r *controller.Reconciler) { r.PowerOner = pwr },
	)
}
