package environments

import (
	"fmt"
	"strings"

	"github.com/codefly-dev/core/resources"
)

// Deployed reports whether this environment is a hosted cell rather than a
// local cluster. It is the split the CLI already draws with IsK3d — a k3d
// cluster is a developer's own machine, anything else is a cluster other people
// reach — read here under the name the rules that depend on it are written in,
// so a rule meant for a cell never fires on a laptop.
func (env *Environment) Deployed() bool {
	if env == nil {
		return false
	}
	return !env.IsK3d()
}

// IngressRoutesFor returns the routes this environment declares for one
// endpoint, in declared order. A route matches when it names the service (by
// bare name or module/service unique) and either names this endpoint or is
// service-wide (empty Endpoint). The per-endpoint field is honored so a route
// meant for one endpoint is not read as another's.
func IngressRoutesFor(env *Environment, module, service, endpoint string) []EnvironmentIngressRoute {
	if env == nil {
		return nil
	}
	unique := resources.ServiceUnique(module, service)
	var matched []EnvironmentIngressRoute
	for _, route := range env.Ingress {
		if route.Service != service && route.Service != unique {
			continue
		}
		if route.Endpoint != "" && route.Endpoint != endpoint {
			continue
		}
		matched = append(matched, route)
	}
	return matched
}

// IngressHosts returns the hosts this environment's routes bind to one endpoint,
// in declared order. It is the render of a route's own question — which hosts
// does the edge answer for — and is deliberately separate from PublicOriginFor,
// which answers what the workload is told it is reached as.
func IngressHosts(env *Environment, module, service, endpoint string) []string {
	var hosts []string
	seen := map[string]struct{}{}
	for _, route := range IngressRoutesFor(env, module, service, endpoint) {
		for _, host := range route.Hosts {
			if _, ok := seen[host]; ok {
				continue
			}
			seen[host] = struct{}{}
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// originDepth bounds a chain of endpoints reached through one another. A chain
// longer than this is a declaration nobody can follow, and the bound is what
// makes a cycle terminate with a named refusal rather than a stack.
const originDepth = 8

// PublicOriginFor resolves the public origin this environment fixes for one
// public endpoint, or refuses by name.
//
// The declaration is resolved, never merely counted. Three forms answer it, in
// this order:
//
//  1. an ingress route naming hosts — the first host is the endpoint's canonical
//     origin, so an operator writes the origin the product answers as first and
//     its aliases after;
//  2. an ingress route naming `via` — the endpoint has no host of its own and is
//     reached through the named endpoint's origin, which is then resolved the
//     same way;
//  3. a declared app host suffix, from which the host is derived exactly as an
//     external endpoint's DNS record is.
//
// Anything else is a refusal, and so is a declaration that exists but resolves
// to nothing: an empty route, a route whose hosts are empty or blank, a host
// that is not a usable web origin (ResolveOrigin), a `via` naming an endpoint
// that itself resolves to nothing, and a `via` chain that comes back round to
// where it started. The presence of a declaration has never been the question —
// a workload is handed an origin or it is not.
func PublicOriginFor(env *Environment, module, service, endpoint string) (string, error) {
	return resolvePublicOrigin(env, module, service, endpoint, nil)
}

func resolvePublicOrigin(env *Environment, module, service, endpoint string, seen []string) (string, error) {
	subject := publicEndpointName(module, service, endpoint)
	if len(seen) >= originDepth {
		return "", fmt.Errorf("%s is reached through a chain of endpoints too long to follow (%s)", subject, strings.Join(seen, " -> "))
	}
	for _, visited := range seen {
		if visited == subject {
			return "", fmt.Errorf("%s is reached through itself (%s -> %s)", subject, strings.Join(seen, " -> "), subject)
		}
	}
	routes := IngressRoutesFor(env, module, service, endpoint)
	for _, route := range routes {
		hosts, via := route.Hosts, strings.TrimSpace(route.Via)
		switch {
		case len(hosts) > 0 && via != "":
			return "", fmt.Errorf("%s is declared both with hosts of its own and as reached through %s; a route answers one or the other",
				subject, via)
		case len(hosts) > 0:
			origin, err := ResolveOrigin(hosts[0])
			if err != nil {
				return "", describeOriginRefusal(fmt.Sprintf("the host declared for %s", subject), err)
			}
			return origin, nil
		case via != "":
			target, err := parsePublicEndpointName(via)
			if err != nil {
				return "", fmt.Errorf("%s is declared as reached through %q, which does not name an endpoint as \"<module>/<service>/<endpoint>\"", subject, via)
			}
			origin, err := resolvePublicOrigin(env, target.module, target.service, target.endpoint, append(seen, subject))
			if err != nil {
				return "", fmt.Errorf("%s is reached through %s, which fixes no origin: %w", subject, via, err)
			}
			return origin, nil
		}
	}
	if len(routes) > 0 {
		return "", fmt.Errorf("the route declared for %s names neither hosts of its own nor an endpoint it is reached through", subject)
	}
	if host := env.AppHost(&resources.ServiceIdentity{Name: service, Module: module}); host != "" {
		origin, err := ResolveOrigin(host)
		if err != nil {
			return "", describeOriginRefusal(fmt.Sprintf("the host derived for %s from dns.app-host-suffix", subject), err)
		}
		return origin, nil
	}
	return "", fmt.Errorf(
		"%s has no public origin: declare an ingress route naming the host it answers as, a route naming the endpoint it is reached through (via), or an app host suffix under dns.app-host-suffix",
		subject)
}

// publicEndpointName is how an endpoint is named in a refusal.
func publicEndpointName(module, service, endpoint string) string {
	return resources.ServiceUnique(module, service) + "/" + endpoint
}

type publicEndpointRef struct{ module, service, endpoint string }

// parsePublicEndpointName reads a "<module>/<service>/<endpoint>" reference.
func parsePublicEndpointName(reference string) (publicEndpointRef, error) {
	parts := strings.Split(strings.TrimSpace(reference), "/")
	if len(parts) != 3 {
		return publicEndpointRef{}, fmt.Errorf("not an endpoint reference")
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return publicEndpointRef{}, fmt.Errorf("not an endpoint reference")
		}
	}
	return publicEndpointRef{module: parts[0], service: parts[1], endpoint: parts[2]}, nil
}

// ValidateIngress refuses a route whose declaration cannot answer the question
// a route exists to answer, independently of any composition: one that names no
// service, one that names both hosts and a `via`, one that names neither, one
// whose hosts are blank, and one whose `via` is not an endpoint reference.
//
// The composition-wide question — does every public endpoint resolve to an
// origin — is answered where the composition is known. This is the half that can
// be answered from the declaration alone, so it is answered on every path that
// reads an environment rather than only on a render.
func (env *Environment) ValidateIngress() error {
	if env == nil {
		return nil
	}
	for index, route := range env.Ingress {
		position := route.Name
		if position == "" {
			position = fmt.Sprintf("entry %d", index+1)
		}
		if strings.TrimSpace(route.Service) == "" {
			return fmt.Errorf("ingress route %s names no service", position)
		}
		via := strings.TrimSpace(route.Via)
		hosts := make([]string, 0, len(route.Hosts))
		for _, host := range route.Hosts {
			if strings.TrimSpace(host) != "" {
				hosts = append(hosts, host)
			}
		}
		switch {
		case len(hosts) > 0 && via != "":
			return fmt.Errorf("ingress route %s names both hosts and via; a route says how its endpoint is reached, one way or the other", position)
		case len(hosts) == 0 && via == "":
			return fmt.Errorf("ingress route %s names neither a host it answers as nor an endpoint it is reached through (via)", position)
		case len(hosts) != len(route.Hosts):
			return fmt.Errorf("ingress route %s names a blank host", position)
		}
		for _, host := range hosts {
			if _, err := ResolveOrigin(host); err != nil {
				return describeOriginRefusal(fmt.Sprintf("the host declared on ingress route %s", position), err)
			}
		}
		if via != "" {
			if _, err := parsePublicEndpointName(via); err != nil {
				return fmt.Errorf("ingress route %s declares via %q, which does not name an endpoint as \"<module>/<service>/<endpoint>\"", position, via)
			}
		}
	}
	return env.SolutionBoundary.validate()
}
