package v1alpha1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestSamplesMatchAPI strictly unmarshals every sample manifest into the API types, catching
// field typos or drift between config/samples and the CRD schema.
func TestSamplesMatchAPI(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob(filepath.Join("..", "..", "config", "samples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches)

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			var crd TempoFullNode
			require.NoError(t, yaml.UnmarshalStrict(data, &crd), "sample does not match the API types")

			require.Equal(t, "TempoFullNode", crd.Kind)
			require.Equal(t, GroupVersion.String(), crd.APIVersion)
			require.NotEmpty(t, crd.Spec.Role)
			require.NotEmpty(t, crd.Spec.ChainSpec.Chain)
			require.NotEmpty(t, crd.Spec.PodTemplate.Image)
			if crd.Spec.Role == NodeRoleValidator {
				require.NotNil(t, crd.Spec.SigningKey, "validator sample must reference a signing key")
				require.LessOrEqual(t, crd.Spec.Replicas, int32(1))
			}
		})
	}
}
