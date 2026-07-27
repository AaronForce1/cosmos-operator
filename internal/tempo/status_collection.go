package tempo

import (
	"errors"
	"strconv"
	"time"

	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// StatusItem is a pod paired with its Tempo node status.
type StatusItem struct {
	Pod    *corev1.Pod
	Status NodeStatus
	TS     time.Time
	Err    error
}

// GetPod returns the pod.
func (status StatusItem) GetPod() *corev1.Pod {
	return status.Pod
}

// GetStatus returns the node status or an error if the status could not be fetched.
func (status StatusItem) GetStatus() (NodeStatus, error) {
	return status.Status, status.Err
}

// Timestamp returns the time when the node status was fetched.
func (status StatusItem) Timestamp() time.Time { return status.TS }

// StatusCollection is a list of pods and the node status associated with the pod.
type StatusCollection []StatusItem

// Len returns the number of items in the collection. Part of the sort.Interface implementation.
func (coll StatusCollection) Len() int { return len(coll) }

// Less implements sort.Interface.
func (coll StatusCollection) Less(i, j int) bool {
	lhs, _ := strconv.Atoi(coll[i].GetPod().Annotations[kube.OrdinalAnnotation])
	rhs, _ := strconv.Atoi(coll[j].GetPod().Annotations[kube.OrdinalAnnotation])
	return lhs < rhs
}

// Swap implements sort.Interface.
func (coll StatusCollection) Swap(i, j int) {
	coll[i], coll[j] = coll[j], coll[i]
}

// UpsertPod updates the pod in the collection or adds an item to the collection if it does not exist.
// All operations are performed in-place.
func UpsertPod(coll *StatusCollection, pod *corev1.Pod) {
	if *coll == nil {
		*coll = make(StatusCollection, 0)
	}
	for i, p := range *coll {
		if p.GetPod().UID == pod.UID {
			item := (*coll)[i]
			item.Pod = pod
			(*coll)[i] = item
			return
		}
	}
	*coll = append(*coll, StatusItem{Pod: pod, TS: time.Now(), Err: errors.New("missing status")})
}

// IntersectPods removes all pods from the collection that are not in the given list.
func IntersectPods(coll *StatusCollection, pods []corev1.Pod) {
	if *coll == nil {
		*coll = make(StatusCollection, 0)
		return
	}

	set := make(map[types.UID]bool)
	for _, pod := range pods {
		set[pod.UID] = true
	}

	var j int
	for _, item := range *coll {
		if set[item.GetPod().UID] {
			(*coll)[j] = item
			j++
		}
	}
	*coll = (*coll)[:j]
}

// Pods returns all pods.
func (coll StatusCollection) Pods() []*corev1.Pod {
	return lo.Map(coll, func(status StatusItem, _ int) *corev1.Pod { return status.GetPod() })
}

// Synced returns all items that are caught up with the chain tip: reachable, not syncing, and
// with a recent latest block (judged as of the time the status was fetched).
func (coll StatusCollection) Synced() StatusCollection {
	var items []StatusItem
	for _, status := range coll {
		if status.Err != nil {
			continue
		}
		if !status.Status.CaughtUp(status.TS) {
			continue
		}
		items = append(items, status)
	}
	return items
}

// SyncedPods returns the pods that are caught up with the chain tip.
func (coll StatusCollection) SyncedPods() []*corev1.Pod {
	return lo.Map(coll.Synced(), func(status StatusItem, _ int) *corev1.Pod { return status.GetPod() })
}
