package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/docent-net/cluster-bare-autoscaler/pkg/nodeops"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

func (r *Reconciler) restoreScheduling(ctx context.Context, name string) {
	if err := nodeops.UncordonNode(ctx, r.Client, name); err != nil {
		slog.Error("Cannot restore scheduling after aborted shutdown", "node", name, "err", err)
	}
}

func skipDrain(p *v1.Pod) bool {
	if _, ok := p.Annotations["kubernetes.io/config.mirror"]; ok {
		return true
	}
	ref := metav1.GetControllerOf(p)
	return ref != nil && ref.Kind == "DaemonSet"
}

// drainBlocker protects unfinished work regardless of CPU load or job origin.
func drainBlocker(p *v1.Pod) error {
	if p.Status.Phase == v1.PodSucceeded || p.Status.Phase == v1.PodFailed {
		return nil
	}
	if p.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict"] == "false" {
		return fmt.Errorf("pod %s/%s forbids eviction", p.Namespace, p.Name)
	}
	if skipDrain(p) {
		return nil
	}
	ref := metav1.GetControllerOf(p)
	if ref == nil || ref.Kind == "Job" {
		return fmt.Errorf("unfinished job or unmanaged pod %s/%s", p.Namespace, p.Name)
	}
	for _, c := range append(append([]v1.Container{}, p.Spec.Containers...), p.Spec.InitContainers...) {
		for name, quantity := range c.Resources.Limits {
			if strings.Contains(string(name), "/") && !quantity.IsZero() {
				return fmt.Errorf("pod %s/%s uses extended resource %s", p.Namespace, p.Name, name)
			}
		}
	}
	return nil
}

// CordonAndDrain waits for evicted pods to disappear and storage to detach.
// PDB denials are retried within the configured timeout; no force deletion is used.
func (r *Reconciler) CordonAndDrain(ctx context.Context, node *nodeops.NodeWrapper) error {
	timeout := r.Cfg.DrainTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	list := func() (*v1.PodList, error) {
		return r.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	}
	pods, err := list()
	if err != nil {
		return err
	}
	for i := range pods.Items {
		if err := drainBlocker(&pods.Items[i]); err != nil {
			return err
		}
	}
	if r.Cfg.DryRun {
		slog.Info("Dry-run: drain prerequisites passed", "node", node.Name)
		return nil
	}
	if err := retry.OnError(retry.DefaultBackoff, apierrors.IsConflict, func() error {
		latest, err := r.Client.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		latest.Spec.Unschedulable = true
		_, err = r.Client.CoreV1().Nodes().Update(ctx, latest, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}
	slog.Info("Node cordoned; waiting for drain", "node", node.Name)
	accepted := map[string]bool{}
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := list()
		if err != nil {
			return false, err
		}
		remaining := 0
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Spec.NodeName != node.Name {
				continue
			} // Fake clients do not apply field selectors.
			if err := drainBlocker(p); err != nil {
				return false, err
			}
			if skipDrain(p) {
				continue
			}
			remaining++
			key := p.Namespace + "/" + p.Name + "/" + string(p.UID)
			if accepted[key] || p.DeletionTimestamp != nil {
				continue
			}
			uid := p.UID
			err := r.Client.PolicyV1().Evictions(p.Namespace).Evict(ctx, &policyv1.Eviction{
				ObjectMeta:    metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace},
				DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}},
			})
			if apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err) {
				continue
			}
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("aborting drain due to eviction failure: %w", err)
			}
			accepted[key] = true
			slog.Info("Eviction accepted; waiting for termination", "pod", key)
		}
		if remaining > 0 {
			return false, nil
		}
		latest, err := r.Client.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(latest.Status.VolumesInUse) > 0 {
			return false, nil
		}
		attachments, err := r.Client.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, a := range attachments.Items {
			if a.Spec.NodeName == node.Name {
				return false, nil
			}
		}
		slog.Info("Drain complete; pods terminated and volumes detached", "node", node.Name)
		return true, nil
	})
}
