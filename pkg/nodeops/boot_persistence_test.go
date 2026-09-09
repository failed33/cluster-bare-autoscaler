package nodeops

import (
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
	"time"
)

func TestBootCooldownSurvivesControllerRestart(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		age    time.Duration
		active bool
	}{{time.Minute, true}, {21 * time.Minute, false}} {
		n := NewNodeWrapper(&v1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationBootedAt: now.Add(-tc.age).UTC().Format(time.RFC3339)}}}, NewNodeStateTracker(), now, NodeAnnotationConfig{}, nil)
		require.Equal(t, tc.active, n.IsInBootCooldown(20*time.Minute))
	}
}
