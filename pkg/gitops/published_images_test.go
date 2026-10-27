package gitops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/cli/pkg/gitops/cellfile"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPublishedCellImagesPreservesBootstrapAndChecksTarget(t *testing.T) {
	cell := cellfile.File{Schema: cellfile.Schema, Component: "product", Environment: "staging",
		Namespaces: []cellfile.Namespace{{Name: "product-store", Workloads: []cellfile.Workload{
			{Service: "store/db", Kind: "Job", Authenticating: "bootstrap", Containers: []cellfile.Container{{Name: "bootstrap", Image: cellfile.Image{Repository: "registry/store/db", Digest: "sha256:bootstrap"}}}},
			{Service: "store/db", Kind: "StatefulSet", Authenticating: "engine", Containers: []cellfile.Container{{Name: "engine", Image: cellfile.Image{Repository: "registry/postgres", Digest: "sha256:engine"}}}},
		}}},
	}
	data, err := yaml.Marshal(cell)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "cell.yaml")
	require.NoError(t, os.WriteFile(path, data, 0600))
	images, err := publishedCellImages(path, "product", "staging")
	require.NoError(t, err)
	require.Equal(t, "registry/store/db@sha256:bootstrap", images["store/db"])
	_, err = publishedCellImages(path, "product", "production")
	require.ErrorContains(t, err, "must belong")
}
