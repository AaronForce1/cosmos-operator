package test

import (
	"testing"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

// HasRoleLabel asserts every resource built from the crd carries the role label.
func HasRoleLabel(t *testing.T, builder func(crd tempov1alpha1.TempoFullNode) []map[string]string) {
	t.Run("sets labels for", func(t *testing.T) {
		var crd tempov1alpha1.TempoFullNode
		crd.Spec.Replicas = 3

		t.Run("role", func(t *testing.T) {
			for _, role := range []tempov1alpha1.NodeRole{
				tempov1alpha1.NodeRoleValidator,
				tempov1alpha1.NodeRoleRPC,
				tempov1alpha1.NodeRoleArchive,
			} {
				crd.Spec.Role = role
				resources := builder(crd)

				for _, resource := range resources {
					require.Equal(t, string(role), resource["tempo.aaronforce.io/role"])
				}
			}
		})
	})
}
