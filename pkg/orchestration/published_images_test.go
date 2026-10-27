package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/services"
	"github.com/stretchr/testify/require"
)

func TestPublishedImagesRefuseMissingOrMutableImagesWithoutPlanning(t *testing.T) {
	b := &Builder{instance: &services.Instance{Identity: &resources.ServiceIdentity{Module: "saas", Name: "api"}}, world: &World{PublishedImages: map[string]string{}}}
	for _, image := range []string{"", "registry/saas/api:latest", "registry/saas/api@sha256:bad"} {
		b.world.PublishedImages["saas/api"] = image
		_, err := b.Plan(context.Background())
		require.ErrorContains(t, err, "refusing a build")
	}
	b.world.PublishedImages["saas/api"] = "registry/saas/api@sha256:" + strings.Repeat("a", 64)
	_, err := b.Plan(context.Background())
	require.NoError(t, err)
	require.Nil(t, b.planned, "published image reuse never contacts a builder or creates a recipe")
}
