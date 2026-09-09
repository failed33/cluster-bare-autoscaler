package strategy

import (
	"context"
	"github.com/docent-net/cluster-bare-autoscaler/pkg/config"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
	mv1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"testing"
	"time"
)

func TestRemainingUsagePreventsUnsafeShutdown(t *testing.T) {
	s := &ResourceAwareScaleDown{
		Cfg: &config.Config{ResourceBufferMemoryPerc: 25},
		NodeLister: func(context.Context) ([]v1.Node, error) {
			return []v1.Node{newNode("pi", "4", "8Gi"), newNode("worker", "8", "16Gi")}, nil
		},
		PodLister: func(context.Context) ([]v1.Pod, error) { return nil, nil },
		MetricsClient: fake.NewSimpleClientset(
			&mv1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: "pi"}, Usage: v1.ResourceList{v1.ResourceMemory: resource.MustParse("5Gi")}},
			&mv1.NodeMetrics{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Usage: v1.ResourceList{v1.ResourceMemory: resource.MustParse("2Gi")}},
		),
	}
	s.MetricsClient.(*fake.Clientset).PrependReactor("list", "nodes", func(kt.Action) (bool, runtime.Object, error) {
		return true, &mv1.NodeMetricsList{Items: []mv1.NodeMetrics{
			{ObjectMeta: metav1.ObjectMeta{Name: "pi"}, Usage: v1.ResourceList{v1.ResourceMemory: resource.MustParse("5Gi")}},
			{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Usage: v1.ResourceList{v1.ResourceMemory: resource.MustParse("2Gi")}},
		}}, nil
	})
	ok, err := s.ShouldScaleDown(context.Background(), "worker")
	require.NoError(t, err)
	require.False(t, ok)
}
func TestMissingLoadIsNotAnIdleNode(t *testing.T) {
	u := NewClusterLoadUtils(kfake.NewSimpleClientset(), "cba", "app=metrics", 9100, time.Second)
	_, _, err := u.FetchClusterLoads(context.Background(), []string{"missing"})
	require.Error(t, err)
}
