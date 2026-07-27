package fullnode

import (
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultUpgradeLeadTime is how long before a hardfork's activation timestamp the operator starts
// rolling pods onto the new image. Nodes must run the new binary BEFORE activation or they fork
// off the network, so the roll must complete inside the lead window.
const defaultUpgradeLeadTime = 6 * time.Hour

func upgradeLeadTime(u tempov1alpha1.ScheduledUpgrade) time.Duration {
	if u.LeadTime != nil {
		return u.LeadTime.Duration
	}
	return defaultUpgradeLeadTime
}

// upgradeRollStart is the instant the operator starts rolling pods onto the upgrade's image.
func upgradeRollStart(u tempov1alpha1.ScheduledUpgrade) time.Time {
	return u.ActivatesAt.Add(-upgradeLeadTime(u))
}

// DesiredImage returns the image all pods should run at time "now": the pod template's image,
// superseded by the last spec.scheduledUpgrades entry whose roll window has started.
// Entries are expected in ascending activatesAt order; out-of-order entries are still handled by
// picking the latest applicable activation.
func DesiredImage(crd *tempov1alpha1.TempoFullNode, now time.Time) string {
	image := crd.Spec.PodTemplate.Image
	var current *tempov1alpha1.ScheduledUpgrade
	for i := range crd.Spec.ScheduledUpgrades {
		u := crd.Spec.ScheduledUpgrades[i]
		if now.Before(upgradeRollStart(u)) {
			continue
		}
		if current == nil || current.ActivatesAt.Time.Before(u.ActivatesAt.Time) {
			current = &crd.Spec.ScheduledUpgrades[i]
		}
	}
	if current != nil {
		image = current.Image
	}
	return image
}

// UpgradeStatus reports the currently desired image and the next pending upgrade for the CRD status.
func UpgradeStatus(crd *tempov1alpha1.TempoFullNode, now time.Time) *tempov1alpha1.UpgradeStatus {
	if len(crd.Spec.ScheduledUpgrades) == 0 {
		return nil
	}
	status := &tempov1alpha1.UpgradeStatus{CurrentImage: DesiredImage(crd, now)}
	var next *tempov1alpha1.ScheduledUpgrade
	for i := range crd.Spec.ScheduledUpgrades {
		u := crd.Spec.ScheduledUpgrades[i]
		if !now.Before(upgradeRollStart(u)) {
			continue
		}
		if next == nil || u.ActivatesAt.Time.Before(next.ActivatesAt.Time) {
			next = &crd.Spec.ScheduledUpgrades[i]
		}
	}
	if next != nil {
		status.NextActivatesAt = ptr(metav1.NewTime(next.ActivatesAt.Time))
		status.NextImage = ptr(next.Image)
	}
	return status
}

// NextUpgradeCheck returns when the reconciler should wake up to start the next upgrade roll,
// or the zero time if no upgrade is pending.
func NextUpgradeCheck(crd *tempov1alpha1.TempoFullNode, now time.Time) time.Time {
	var soonest time.Time
	for _, u := range crd.Spec.ScheduledUpgrades {
		start := upgradeRollStart(u)
		if now.Before(start) && (soonest.IsZero() || start.Before(soonest)) {
			soonest = start
		}
	}
	return soonest
}
