package gitops

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/codefly-dev/cli/pkg/environments"
	"github.com/codefly-dev/cli/pkg/solutionrun"
	"github.com/codefly-dev/core/resources"
)

// publicOriginPrefix carries the public web origin the environment fixes for one
// of a service's public endpoints.
//
// It deliberately starts with neither resources.EndpointPrefix nor
// resources.SelfEndpointPrefix, for the reason core states for the latter: a
// reader collecting one family of carriers must never pick up another's. The
// three are three different addresses of the same endpoint — the address the
// service listens on (CODEFLY__ENDPOINT__), the in-cluster address its peers
// reach it at (CODEFLY__SELF_ENDPOINT__), and the origin the outside world names
// it by, which only an operator can know and which is this one.
//
// Its natural home is core, beside the other two, once a core release carries
// it; until then the CLI owns the name, as it already owns the derivation — the
// ingress and app-host declarations it is derived from are the CLI's
// (pkg/environments), not core's.
const publicOriginPrefix = "CODEFLY__PUBLIC_ORIGIN"

// publicOriginCarrierKey names the carrier with the same module/service/name/api
// normalization core gives the endpoint carriers, so the three addresses of one
// endpoint are recognisably the same endpoint.
func publicOriginCarrierKey(info *resources.EndpointInformation) string {
	return publicOriginPrefix + "__" + resources.EndpointAsEnvironmentVariableKeyBase(info)
}

// isPublicOriginCarrier reports whether a key is one of these carriers.
func isPublicOriginCarrier(key string) bool {
	return strings.HasPrefix(key, publicOriginPrefix+"__")
}

// publicOriginCarriers derives one carrier per public endpoint the service
// serves, holding the origin the environment fixes for it.
//
// On a deployed environment an endpoint whose origin does not resolve is a
// refusal: a workload the outside world reaches has to be able to name the
// origin it is reached by, and the render is the last place an operator can
// supply one. What counts as resolving is environments.PublicOriginFor's
// decision — hosts of its own, an endpoint it is reached through, or a derived
// app host — and a declaration that exists but resolves to nothing refuses
// exactly like no declaration at all.
//
// A local cluster is one developer's machine. Nothing is refused there, and a
// declaration that does resolve is still carried.
//
// An endpoint that lives outside the system carries nothing and is never
// refused: the service does not serve it, so it has no origin of its own.
func publicOriginCarriers(module string, service *resources.Service, env *environments.Environment) (map[string]string, error) {
	if service == nil {
		return nil, nil
	}
	carriers := map[string]string{}
	for _, endpoint := range publicEndpoints(service) {
		origin, err := environments.PublicOriginFor(env, module, service.Name, endpoint.Name)
		if err == nil && env.Deployed() {
			// A cell is reached from outside it, so an origin naming the machine
			// the workload runs on is no origin at all here. A local cluster IS
			// reached at one, which is why the question is asked only here.
			if public := environments.RequirePublicOrigin(origin); public != nil {
				err = fmt.Errorf("the origin fixed for %s/%s/%s %s",
					module, service.Name, endpoint.Name, environments.OriginRefusalReason(public))
			}
		}
		if err != nil {
			if env.Deployed() {
				return nil, fmt.Errorf("environment %q: %w", env.Name, err)
			}
			continue
		}
		info := &resources.EndpointInformation{
			Module: module, Service: service.Name, Name: endpoint.Name, API: endpoint.API,
		}
		carriers[publicOriginCarrierKey(info)] = origin
	}
	if len(carriers) == 0 {
		return nil, nil
	}
	return carriers, nil
}

// publicEndpoints are the endpoints of a service that something outside the
// workspace reaches. An endpoint that lives outside the system is not one the
// service serves, so it is not among them.
func publicEndpoints(service *resources.Service) []*resources.Endpoint {
	var public []*resources.Endpoint
	for _, endpoint := range service.Endpoints {
		if endpoint == nil || endpoint.Visibility != resources.VisibilityPublic || endpoint.External() {
			continue
		}
		public = append(public, endpoint)
	}
	return public
}

// requirePublicOriginsFixed holds every public endpoint of the composition to
// the origin rule at once, before a render builds anything, and reports all of
// them together so an operator writes one set of declarations rather than
// discovering them one render at a time.
//
// It is the same question publicOriginCarriers answers per service, asked over
// the whole composition, and through the same resolver. It also asks the half
// that needs the composition: an endpoint reached through another (`via`) must
// name an endpoint that exists and is public, so a route cannot point at a
// service the composition does not have.
//
// A module that cannot be loaded is skipped: the render loads every module it
// renders and fails on one it cannot, so refusing here would report the wrong
// fault for a module this composition is not rendering. A service of a loaded
// module that cannot be read refuses — its endpoint declarations are what this
// rule is judged against.
func requirePublicOriginsFixed(ctx context.Context, workspace *resources.Workspace, env *environments.Environment) error {
	if !env.Deployed() {
		return nil
	}
	public := map[string]bool{}
	type pending struct {
		name string
		via  string
	}
	var endpoints []pending
	var refusals []string
	for _, ref := range workspace.Modules {
		mod, err := workspace.LoadModuleFromReference(ctx, ref)
		if err != nil {
			continue
		}
		for _, svcRef := range mod.ServiceReferences {
			service, err := workspace.LoadService(ctx, &resources.ServiceWithModule{Name: svcRef.Name, Module: mod.Name})
			if err != nil {
				return fmt.Errorf("cannot read the endpoint declarations of %s: %w",
					resources.ServiceUnique(mod.Name, svcRef.Name), err)
			}
			for _, endpoint := range publicEndpoints(service) {
				name := resources.ServiceUnique(mod.Name, service.Name) + "/" + endpoint.Name
				public[name] = true
				via := ""
				for _, route := range environments.IngressRoutesFor(env, mod.Name, service.Name, endpoint.Name) {
					if trimmed := strings.TrimSpace(route.Via); trimmed != "" {
						via = trimmed
						break
					}
				}
				endpoints = append(endpoints, pending{name: name, via: via})
				origin, err := environments.PublicOriginFor(env, mod.Name, service.Name, endpoint.Name)
				switch {
				case err != nil:
					refusals = append(refusals, err.Error())
				default:
					if public := environments.RequirePublicOrigin(origin); public != nil {
						refusals = append(refusals, fmt.Sprintf("the origin fixed for %s %s",
							name, environments.OriginRefusalReason(public)))
					}
				}
			}
		}
	}
	// A via target has to be a public endpoint this composition serves. Checked
	// after the walk, when every public endpoint is known.
	for _, endpoint := range endpoints {
		if endpoint.via == "" || public[endpoint.via] {
			continue
		}
		refusals = append(refusals, fmt.Sprintf(
			"%s is declared as reached through %s, which is not a public endpoint of this composition",
			endpoint.name, endpoint.via))
	}
	if len(refusals) == 0 {
		return nil
	}
	sort.Strings(refusals)
	return fmt.Errorf("environment %q fixes no public origin for %d of the public endpoints this composition serves:\n  %s",
		env.Name, len(refusals), strings.Join(refusals, "\n  "))
}

// withPublicOriginCarriers adds the public-origin carriers to what the render
// already derived for a service. The injection it was handed is shared with
// every other projection of the same render, so it is cloned rather than written
// into.
func withPublicOriginCarriers(
	module string,
	service *resources.Service,
	env *environments.Environment,
	injection serviceInjection,
) (serviceInjection, error) {
	carriers, err := publicOriginCarriers(module, service, env)
	if err != nil {
		return serviceInjection{}, err
	}
	if len(carriers) == 0 {
		return injection, nil
	}
	merged := solutionrun.ServiceInjection{
		Public:  make(map[string]string, len(injection.Public)+len(carriers)),
		Secrets: injection.Secrets,
	}
	maps.Copy(merged.Public, injection.Public)
	maps.Copy(merged.Public, carriers)
	return merged, nil
}

// hostSurface projects the environment's declared solution boundary into the
// surface the boundary rule reads. An undeclared boundary is an empty surface,
// which permits nothing.
func hostSurface(env *environments.Environment) solutionrun.HostSurface {
	if env == nil || env.SolutionBoundary == nil {
		return nil
	}
	surface := make(solutionrun.HostSurface, 0, len(env.SolutionBoundary.HostSurface))
	for _, entry := range env.SolutionBoundary.HostSurface {
		surface = append(surface, solutionrun.HostSurfaceEntry{
			Module:   entry.Module(),
			Service:  entry.Name(),
			Endpoint: strings.TrimSpace(entry.Endpoint),
		})
	}
	return surface
}
