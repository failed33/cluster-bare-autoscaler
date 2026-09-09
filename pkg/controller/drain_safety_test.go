package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docent-net/cluster-bare-autoscaler/pkg/config"
	"github.com/docent-net/cluster-bare-autoscaler/pkg/nodeops"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func TestDrainSafety(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kind         string
		gpu          bool
		deny         bool
		remove       bool
		attachment   bool
		wantOK       bool
		wantEviction bool
	}{
		{name: "wait for termination", kind: "ReplicaSet", wantEviction: true},
		{name: "PDB denial", kind: "ReplicaSet", deny: true, wantEviction: true},
		{name: "storage detach", kind: "ReplicaSet", remove: true, attachment: true, wantEviction: true},
		{name: "terminated and detached", kind: "ReplicaSet", remove: true, wantOK: true, wantEviction: true},
		{name: "unfinished job", kind: "Job"},
		{name: "unmanaged work"},
		{name: "GPU workload", kind: "ReplicaSet", gpu: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yes := true
			pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "default", UID: "uid"}, Spec: v1.PodSpec{NodeName: "worker"}, Status: v1.PodStatus{Phase: v1.PodRunning}}
			if tc.kind != "" {
				pod.OwnerReferences = []metav1.OwnerReference{{Kind: tc.kind, Name: "owner", Controller: &yes}}
			}
			if tc.gpu {
				pod.Spec.Containers = []v1.Container{{Resources: v1.ResourceRequirements{Limits: v1.ResourceList{"amd.com/gpu": resource.MustParse("1")}}}}
			}
			node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			client := fake.NewSimpleClientset(node, pod)
			if tc.attachment {
				_, err := client.StorageV1().VolumeAttachments().Create(context.Background(), &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: storagev1.VolumeAttachmentSpec{NodeName: "worker"}}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			calls := 0
			client.PrependReactor("create", "pods", func(a kt.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "eviction" {
					return false, nil, nil
				}
				calls++
				if tc.deny {
					return true, nil, apierrors.NewTooManyRequests("PDB", 1)
				}
				if tc.remove {
					require.NoError(t, client.Tracker().Delete(v1.SchemeGroupVersion.WithResource("pods"), "default", "work"))
				}
				return true, nil, nil
			})
			r := &Reconciler{Client: client, Cfg: &config.Config{DrainTimeout: 2200 * time.Millisecond}}
			err := r.CordonAndDrain(context.Background(), &nodeops.NodeWrapper{Node: node})
			if tc.wantOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.wantEviction, calls > 0)
		})
	}
}

func TestShutdownFailureDoesNotMarkOff(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			client := fake.NewSimpleClientset(node)
			state := nodeops.NewNodeStateTracker()
			r := &Reconciler{Client: client, Cfg: &config.Config{DryRun: dry}, State: state, ScaleDownStrategy: allowDown{}, Shutdowner: rejectShutdown{}}
			got := r.MaybeScaleDown(context.Background(), []*nodeops.NodeWrapper{{Node: node}})
			require.Equal(t, dry, got)
			require.False(t, state.IsPoweredOff("worker"))
			latest, err := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
			require.NoError(t, err)
			require.False(t, latest.Spec.Unschedulable)
			require.Empty(t, latest.Annotations[nodeops.AnnotationPoweredOff])
		})
	}
}

type allowDown struct{}

func (allowDown) Name() string                                          { return "test" }
func (allowDown) ShouldScaleDown(context.Context, string) (bool, error) { return true, nil }

type rejectShutdown struct{}

func (rejectShutdown) Shutdown(context.Context, string) error {
	return fmt.Errorf("host rejected shutdown")
}
