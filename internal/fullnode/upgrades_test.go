package fullnode

import (
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDesiredImage(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	crd := defaultCRD()
	crd.Spec.PodTemplate.Image = "tempo:v1"
	crd.Spec.ScheduledUpgrades = []tempov1alpha1.ScheduledUpgrade{
		{ActivatesAt: metav1.NewTime(base), Image: "tempo:v2"},
		{ActivatesAt: metav1.NewTime(base.Add(24 * time.Hour)), Image: "tempo:v3", LeadTime: &metav1.Duration{Duration: time.Hour}},
	}

	t.Run("before any roll window", func(t *testing.T) {
		require.Equal(t, "tempo:v1", DesiredImage(&crd, base.Add(-7*time.Hour)))
	})

	t.Run("inside default 6h lead window", func(t *testing.T) {
		require.Equal(t, "tempo:v2", DesiredImage(&crd, base.Add(-5*time.Hour)))
	})

	t.Run("after activation", func(t *testing.T) {
		require.Equal(t, "tempo:v2", DesiredImage(&crd, base.Add(time.Hour)))
	})

	t.Run("second upgrade with custom lead time", func(t *testing.T) {
		// 2h before the second activation is outside its 1h lead window.
		require.Equal(t, "tempo:v2", DesiredImage(&crd, base.Add(22*time.Hour)))
		// 30m before is inside.
		require.Equal(t, "tempo:v3", DesiredImage(&crd, base.Add(23*time.Hour+30*time.Minute)))
	})

	t.Run("no upgrades", func(t *testing.T) {
		plain := defaultCRD()
		plain.Spec.PodTemplate.Image = "tempo:v1"
		require.Equal(t, "tempo:v1", DesiredImage(&plain, base))
	})
}

func TestUpgradeStatus(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	crd := defaultCRD()
	crd.Spec.PodTemplate.Image = "tempo:v1"

	t.Run("no upgrades", func(t *testing.T) {
		require.Nil(t, UpgradeStatus(&crd, base))
	})

	crd.Spec.ScheduledUpgrades = []tempov1alpha1.ScheduledUpgrade{
		{ActivatesAt: metav1.NewTime(base), Image: "tempo:v2"},
		{ActivatesAt: metav1.NewTime(base.Add(24 * time.Hour)), Image: "tempo:v3"},
	}

	t.Run("pending upgrade", func(t *testing.T) {
		status := UpgradeStatus(&crd, base.Add(-10*time.Hour))
		require.Equal(t, "tempo:v1", status.CurrentImage)
		require.Equal(t, base, status.NextActivatesAt.Time)
		require.Equal(t, "tempo:v2", *status.NextImage)
	})

	t.Run("between upgrades", func(t *testing.T) {
		status := UpgradeStatus(&crd, base.Add(time.Hour))
		require.Equal(t, "tempo:v2", status.CurrentImage)
		require.Equal(t, "tempo:v3", *status.NextImage)
	})

	t.Run("all upgrades applied", func(t *testing.T) {
		status := UpgradeStatus(&crd, base.Add(48*time.Hour))
		require.Equal(t, "tempo:v3", status.CurrentImage)
		require.Nil(t, status.NextActivatesAt)
		require.Nil(t, status.NextImage)
	})
}

func TestNextUpgradeCheck(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	crd := defaultCRD()
	crd.Spec.ScheduledUpgrades = []tempov1alpha1.ScheduledUpgrade{
		{ActivatesAt: metav1.NewTime(base), Image: "tempo:v2"},
	}

	// Roll starts at activatesAt - 6h.
	got := NextUpgradeCheck(&crd, base.Add(-10*time.Hour))
	require.Equal(t, base.Add(-6*time.Hour), got)

	// Inside the window there is nothing left to wait for.
	require.True(t, NextUpgradeCheck(&crd, base.Add(-time.Hour)).IsZero())
}
