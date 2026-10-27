package gitops

import (
	"fmt"
	"github.com/codefly-dev/cli/pkg/gitops/cellfile"
	"os"
)

// publishedCellImages reuses the owning generator's deployment inventory.
// A store's bootstrap Job is the image its builder emits; its StatefulSet
// engine is independently pinned by the agent.
func publishedCellImages(path, workspace, environment string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cell, err := cellfile.Parse(data)
	if err != nil {
		return nil, err
	}
	if cell.Component != workspace || cell.Environment != environment {
		return nil, fmt.Errorf("image inventory must belong to %s/%s", workspace, environment)
	}
	images := map[string]string{}
	jobs := map[string]bool{}
	for _, namespace := range cell.Namespaces {
		for _, workload := range namespace.Workloads {
			if jobs[workload.Service] {
				continue
			}
			for _, container := range workload.Containers {
				if container.Name != workload.Authenticating {
					continue
				}
				images[workload.Service] = container.Image.Repository + "@" + container.Image.Digest
				jobs[workload.Service] = workload.Kind == "Job"
			}
		}
	}
	return images, nil
}
