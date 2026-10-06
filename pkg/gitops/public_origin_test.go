package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/cli/pkg/environments"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

const frontendOriginKey = "CODEFLY__PUBLIC_ORIGIN__PAYMENTS__FRONTEND__HTTP__HTTP"

// publicFrontend serves one public endpoint and one the module keeps to itself.
func publicFrontend() *resources.Service {
	return &resources.Service{
		Name: "frontend",
		Endpoints: []*resources.Endpoint{
			{Name: "http", API: "http", Visibility: resources.VisibilityPublic},
			{Name: "grpc", API: "grpc", Visibility: resources.VisibilityModule},
		},
	}
}

func cellEnvironment() *environments.Environment {
	return &environments.Environment{Name: "staging", Namespace: "payments"}
}

func productRoute(hosts ...string) environments.EnvironmentIngressRoute {
	return environments.EnvironmentIngressRoute{Name: "product", Service: "frontend", Endpoint: "http", Hosts: hosts}
}

// rewriteFile changes a rendered file, which is how these tests express what an
// agent or an operator did to the tree.
func rewriteFile(t *testing.T, path string, replace func(string) string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(replace(string(data))), 0o644))
}

func boundaryWorkspace(t *testing.T) *resources.Workspace {
	t.Helper()
	workspace, err := resources.LoadWorkspaceFromDir(t.Context(), filepath.Join("..", "solutionrun", "testdata", "solution-boundary"))
	require.NoError(t, err)
	return workspace
}

// The origin a service answers as is operator configuration, so it is the
// environment's declaration that supplies it — and only for the endpoint the
// route names.
func TestPublicOriginCarrierComesFromTheIngressDeclaration(t *testing.T) {
	env := cellEnvironment()
	env.Ingress = []environments.EnvironmentIngressRoute{productRoute("app.example.com", "www.example.com")}

	carriers, err := publicOriginCarriers("payments", publicFrontend(), env)
	require.NoError(t, err)
	require.Equal(t, map[string]string{frontendOriginKey: "https://app.example.com"}, carriers,
		"the first declared host is the canonical origin, and a module-visibility endpoint has no public origin at all")
}

// An endpoint with no host of its own is reached through another endpoint's
// origin, and that is what its workload is told. This is the declaration that
// makes the rule answerable for a backend behind a front door, rather than
// forcing a hostname onto something that has none.
func TestPublicOriginCarrierFollowsVia(t *testing.T) {
	env := cellEnvironment()
	env.Ingress = []environments.EnvironmentIngressRoute{
		{Name: "product", Service: "host/frontend", Endpoint: "http", Hosts: []string{"app.example.com"}},
		{Name: "guest", Service: "payments/frontend", Endpoint: "http", Via: "host/frontend/http"},
	}
	carriers, err := publicOriginCarriers("payments", publicFrontend(), env)
	require.NoError(t, err)
	require.Equal(t, map[string]string{frontendOriginKey: "https://app.example.com"}, carriers)
}

// With no route, the environment's declared app host suffix is the declaration,
// derived exactly as an external endpoint's DNS record is.
func TestPublicOriginCarrierFallsBackToTheAppHostSuffix(t *testing.T) {
	env := cellEnvironment()
	env.DNS = &environments.EnvironmentDNS{AppHostSuffix: "cell.example.com"}

	carriers, err := publicOriginCarriers("payments", publicFrontend(), env)
	require.NoError(t, err)
	require.Equal(t, map[string]string{frontendOriginKey: "https://frontend-payments.cell.example.com"}, carriers)
}

// A declaration that exists but resolves to nothing refuses exactly like no
// declaration at all. Presence was never the question: a workload is handed an
// origin or it is not.
func TestDeployedRenderRefusesAnUnresolvedDeclaration(t *testing.T) {
	for name, route := range map[string]environments.EnvironmentIngressRoute{
		"nothing declared":   {},
		"an empty route":     {Name: "product"},
		"another service":    {Name: "product", Service: "absent", Hosts: []string{"app.example.com"}},
		"another endpoint":   {Name: "product", Service: "payments/frontend", Endpoint: "absent", Hosts: []string{"app.example.com"}},
		"no hosts":           {Name: "product", Service: "payments/frontend", Endpoint: "http"},
		"a blank host":       {Name: "product", Service: "payments/frontend", Endpoint: "http", Hosts: []string{" "}},
		"an unusable host":   {Name: "product", Service: "payments/frontend", Endpoint: "http", Hosts: []string{"localhost"}},
		"a via to nowhere":   {Name: "product", Service: "payments/frontend", Endpoint: "http", Via: "host/frontend/http"},
		"a malformed via":    {Name: "product", Service: "payments/frontend", Endpoint: "http", Via: "host/frontend"},
		"hosts and via both": {Name: "product", Service: "payments/frontend", Endpoint: "http", Hosts: []string{"app.example.com"}, Via: "host/frontend/http"},
	} {
		t.Run(name, func(t *testing.T) {
			env := cellEnvironment()
			env.Ingress = []environments.EnvironmentIngressRoute{route}
			_, err := publicOriginCarriers("payments", publicFrontend(), env)
			require.Error(t, err)
			require.Contains(t, err.Error(), "payments/frontend/http")
		})
	}
}

// An endpoint reached through itself, directly or round a chain, is a
// declaration nobody can follow. It terminates with a named refusal.
func TestDeployedRenderRefusesAnOriginCycle(t *testing.T) {
	env := cellEnvironment()
	env.Ingress = []environments.EnvironmentIngressRoute{
		{Name: "a", Service: "payments/frontend", Endpoint: "http", Via: "payments/other/http"},
		{Name: "b", Service: "payments/other", Endpoint: "http", Via: "payments/frontend/http"},
	}
	_, err := publicOriginCarriers("payments", publicFrontend(), env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reached through itself")
}

// A local cluster is one machine, and the origin a developer's browser uses to
// reach it IS a loopback one — so the declaration is carried, and nothing is
// refused. That is the whole reason "is this a well-formed origin" and "is this
// a public origin" are two questions: the second is a cell's, and asking it here
// would refuse a local composition for declaring the truth about itself.
func TestLocalRenderCarriesItsOwnOriginAndRefusesNothing(t *testing.T) {
	local := &environments.Environment{
		Name:    "local",
		Cluster: &environments.EnvironmentCluster{Kind: environments.ClusterKindK3d},
		Ingress: []environments.EnvironmentIngressRoute{productRoute("app.shop.localhost")},
	}
	carriers, err := publicOriginCarriers("payments", publicFrontend(), local)
	require.NoError(t, err)
	require.Equal(t, map[string]string{frontendOriginKey: "https://app.shop.localhost"}, carriers)

	// The same host on a cell is refused, naming the endpoint and the reason.
	cell := cellEnvironment()
	cell.Ingress = []environments.EnvironmentIngressRoute{productRoute("app.shop.localhost")}
	_, err = publicOriginCarriers("payments", publicFrontend(), cell)
	require.Error(t, err)
	require.Contains(t, err.Error(), "payments/frontend/http")
	require.Contains(t, err.Error(), "names the machine the workload runs on")

	// And a composition that declares nothing at all is not refused locally.
	bare := &environments.Environment{Name: "local", Cluster: &environments.EnvironmentCluster{Kind: environments.ClusterKindK3d}}
	carriers, err = publicOriginCarriers("payments", publicFrontend(), bare)
	require.NoError(t, err)
	require.Empty(t, carriers)
}

// An endpoint that lives outside the system is not served by this workload, so
// it has no origin of its own and is never refused for having none.
func TestPublicOriginIgnoresAnExternalEndpoint(t *testing.T) {
	service := &resources.Service{
		Name: "payments-gateway",
		Endpoints: []*resources.Endpoint{
			{Name: "rest", API: "rest", Visibility: resources.VisibilityPublic, Location: resources.LocationExternal},
		},
	}
	carriers, err := publicOriginCarriers("payments", service, cellEnvironment())
	require.NoError(t, err)
	require.Empty(t, carriers)
}

// The composition-wide pass reports every endpoint at once, so an operator
// writes one set of declarations rather than discovering them one render at a
// time — and a via target has to be a public endpoint the composition serves.
func TestDeployedRenderReportsEveryUnresolvedEndpoint(t *testing.T) {
	workspace := boundaryWorkspace(t)
	err := requirePublicOriginsFixed(t.Context(), workspace, cellEnvironment())
	require.Error(t, err)
	require.Contains(t, err.Error(), "host/frontend/http")
	require.Contains(t, err.Error(), "consumer/backend/http")

	fixed := cellEnvironment()
	fixed.Ingress = []environments.EnvironmentIngressRoute{
		{Name: "product", Service: "host/frontend", Endpoint: "http", Hosts: []string{"app.example.com"}},
		{Name: "guest", Service: "consumer/backend", Endpoint: "http", Via: "host/frontend/http"},
	}
	require.NoError(t, requirePublicOriginsFixed(t.Context(), workspace, fixed))

	absent := cellEnvironment()
	absent.Ingress = []environments.EnvironmentIngressRoute{
		{Name: "product", Service: "host/frontend", Endpoint: "http", Hosts: []string{"app.example.com"}},
		{Name: "guest", Service: "consumer/backend", Endpoint: "http", Via: "host/absent/http"},
	}
	err = requirePublicOriginsFixed(t.Context(), workspace, absent)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a public endpoint of this composition")

	suffixed := cellEnvironment()
	suffixed.DNS = &environments.EnvironmentDNS{AppHostSuffix: "cell.example.com"}
	require.NoError(t, requirePublicOriginsFixed(t.Context(), workspace, suffixed))
}

// The render has to call the precondition, not merely have one: removing the
// call left every helper test green.
func TestRenderCallsTheOriginPrecondition(t *testing.T) {
	_, err := deriveRenderInjections(t.Context(), boundaryWorkspace(t), cellEnvironment(), nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "fixes no public origin")
	require.Contains(t, err.Error(), "host/frontend/http")
}

// Through the real projection: the origin reaches the workload in the ConfigMap
// its container already loads its configuration from.
func TestPublicOriginReachesTheRenderedConfigMap(t *testing.T) {
	env := storeEnvironment()
	env.Ingress = []environments.EnvironmentIngressRoute{productRoute("app.example.com")}
	service := publicFrontend()
	scope := scopeOf(env)
	scope.Module = "payments"

	root := t.TempDir()
	writeConsumerTree(t, root, env.Name, env.Namespace, service.Name, "documents.example.test:8080")
	require.NoError(t, projectServiceConfiguration(t.Context(), root, service, env, scope, serviceInjection{}))
	require.Equal(t, "https://app.example.com", configMapData(t, buildOverlay(t, root, env.Name))[frontendOriginKey])
}

// A declared origin is required delivery, not an offer. An overlay that unbinds
// the configuration the workload loads, and a container that reads none, each
// leave the declaration undelivered — so the render refuses by name instead of
// skipping it.
func TestPublicOriginMustReachTheEffectiveWorkload(t *testing.T) {
	for name, shape := range map[string]string{
		"the overlay unbinds the configuration":                 "patches:\n  - target:\n      kind: Deployment\n      name: frontend\n    patch: |-\n      - op: remove\n        path: /spec/template/spec/containers/0/envFrom\n",
		"the overlay unbinds it but keeps the service identity": "patches:\n  - target:\n      kind: Deployment\n      name: frontend\n    patch: |-\n      - op: remove\n        path: /spec/template/spec/containers/0/envFrom\n      - op: add\n        path: /spec/template/spec/containers/0/env\n        value:\n          - name: CODEFLY__SERVICE\n            value: frontend\n",
	} {
		t.Run(name, func(t *testing.T) {
			env := storeEnvironment()
			env.Ingress = []environments.EnvironmentIngressRoute{productRoute("app.example.com")}
			scope := scopeOf(env)
			scope.Module = "payments"
			root := t.TempDir()
			writeConsumerTree(t, root, env.Name, env.Namespace, "frontend", "documents.example.test:8080")
			patchOverlay(t, root, env.Name, shape)

			err := projectServiceConfiguration(t.Context(), root, publicFrontend(), env, scope, serviceInjection{})
			require.Error(t, err, "the declared origin no longer reaches the workload")
		})
	}
}

// And the same when nothing in the rendered unit claims the service at all: a
// workload that reads no Codefly configuration cannot be handed the origin an
// operator declared for it, so the contradiction is refused rather than skipped.
func TestPublicOriginRefusesAWorkloadThatClaimsNoService(t *testing.T) {
	env := storeEnvironment()
	env.Ingress = []environments.EnvironmentIngressRoute{productRoute("app.example.com")}
	scope := scopeOf(env)
	scope.Module = "payments"
	root := t.TempDir()
	writeConsumerTree(t, root, env.Name, env.Namespace, "frontend", "documents.example.test:8080")
	rewriteFile(t, filepath.Join(root, "base", "config-map.yaml"), func(text string) string {
		return strings.ReplaceAll(text, "  CODEFLY__SERVICE: \"frontend\"\n", "")
	})

	err := projectServiceConfiguration(t.Context(), root, publicFrontend(), env, scope, serviceInjection{})
	require.Error(t, err)
}
