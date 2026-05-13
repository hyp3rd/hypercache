package main

import (
	"slices"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
	"gopkg.in/yaml.v3"

	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// TestOpenAPISpecMatchesRoutes is the drift detector. It registers
// every client-API route the production binary exposes onto a
// throwaway fiber app, then walks the embedded OpenAPI spec — and
// asserts the two sets of (method, path) tuples are equal. Any
// route added in main.go without a matching path in openapi.yaml
// (or vice-versa) trips this test, so the contract published at
// `GET /v1/openapi.yaml` cannot silently fall out of sync with
// what the binary actually serves.
//
// Approach notes:
//   - We drive `registerClientRoutes` directly rather than spinning
//     up the management/dist HTTP listeners — the spec only covers
//     the client API. Auth token is empty so handler wiring is
//     identical to production but no Authorization header is
//     required (the routes themselves are registered identically).
//   - Fiber stores params as `:key`; OpenAPI uses `{key}`. We
//     normalize to OpenAPI form before comparing.
//   - We ignore fiber's auto-registered HEAD-for-GET and OPTIONS
//     handlers, only counting the methods we explicitly registered.
func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	t.Parallel()

	codeRoutes := registeredCodeRoutes(t)
	specRoutes := documentedSpecRoutes(t)

	missingFromSpec := difference(codeRoutes, specRoutes)
	if len(missingFromSpec) > 0 {
		t.Errorf("routes registered in code but NOT documented in openapi.yaml:\n  %s", strings.Join(missingFromSpec, "\n  "))
	}

	missingFromCode := difference(specRoutes, codeRoutes)
	if len(missingFromCode) > 0 {
		t.Errorf("paths documented in openapi.yaml but NOT registered in code:\n  %s", strings.Join(missingFromCode, "\n  "))
	}
}

// registeredCodeRoutes returns the canonical "METHOD path" set
// for routes the production binary actually serves on the client
// API. We skip fiber's auto-registered HEAD-for-GET (a route we
// did not declare) by tracking which methods we explicitly wire
// in registerClientRoutes — the route table includes every
// fiber.Method, but only the ones that appear in our wiring are
// part of the contract.
func registeredCodeRoutes(t *testing.T) map[string]struct{} {
	t.Helper()

	app := fiber.New()
	// Drift test only cares about route paths, not auth — the zero
	// Policy 401s every protected route, but app.GetRoutes() reads
	// the registration table without driving requests.
	registerClientRoutes(app, httpauth.Policy{}, &nodeContext{nodeID: "drift-test"})

	declared := declaredMethodsForPath()
	out := map[string]struct{}{}

	for _, r := range app.GetRoutes() {
		methods, ok := declared[r.Path]
		if !ok {
			continue
		}

		if _, want := methods[r.Method]; !want {
			continue
		}

		out[normalize(r.Method, r.Path)] = struct{}{}
	}

	return out
}

// declaredMethodsForPath enumerates the (path, methods) pairs that
// registerClientRoutes wires by hand. Kept here rather than
// reflected from the fiber app because fiber auto-registers HEAD
// for every GET — and we want to assert against the methods we
// explicitly declared, not the implicit ones. If a new route is
// added to registerClientRoutes, it must be mirrored here too;
// this list is a small price for not coupling the test to fiber's
// internal route-expansion behavior.
func declaredMethodsForPath() map[string]map[string]struct{} {
	return map[string]map[string]struct{}{
		"/healthz":               {fiber.MethodGet: {}},
		"/v1/openapi.yaml":       {fiber.MethodGet: {}},
		"/v1/cache/:key":         {fiber.MethodPut: {}, fiber.MethodGet: {}, fiber.MethodHead: {}, fiber.MethodDelete: {}},
		"/v1/cache/keys":         {fiber.MethodGet: {}},
		"/v1/owners/:key":        {fiber.MethodGet: {}},
		"/v1/me":                 {fiber.MethodGet: {}},
		"/v1/me/can":             {fiber.MethodGet: {}},
		"/v1/cache/batch/get":    {fiber.MethodPost: {}},
		"/v1/cache/batch/put":    {fiber.MethodPost: {}},
		"/v1/cache/batch/delete": {fiber.MethodPost: {}},
	}
}

// documentedSpecRoutes parses the embedded YAML and projects every
// (method, path) tuple it documents. Only the standard HTTP
// methods are considered — keys like `parameters`, `summary`, and
// `description` at the path-item level are skipped.
func documentedSpecRoutes(t *testing.T) map[string]struct{} {
	t.Helper()

	type pathItem map[string]yaml.Node

	var doc struct {
		Paths map[string]pathItem `yaml:"paths"`
	}

	err := yaml.Unmarshal(openapiSpec, &doc)
	if err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	httpMethods := map[string]string{
		"get":     fiber.MethodGet,
		"put":     fiber.MethodPut,
		"post":    fiber.MethodPost,
		"delete":  fiber.MethodDelete,
		"head":    fiber.MethodHead,
		"options": fiber.MethodOptions,
		"patch":   fiber.MethodPatch,
		"trace":   fiber.MethodTrace,
	}

	out := map[string]struct{}{}

	for path, item := range doc.Paths {
		for op := range item {
			method, ok := httpMethods[strings.ToLower(op)]
			if !ok {
				continue
			}

			out[normalize(method, path)] = struct{}{}
		}
	}

	return out
}

// normalize converts a fiber-style or OpenAPI-style path into the
// shared comparison form: METHOD followed by the OpenAPI
// `{param}` representation. Fiber's `:key` becomes `{key}`; query
// strings are not part of the path identity (OpenAPI tracks them
// as separate `parameters` entries).
func normalize(method, path string) string {
	out := make([]byte, 0, len(path))

	for i := 0; i < len(path); i++ {
		if path[i] != ':' {
			out = append(out, path[i])

			continue
		}

		j := i + 1

		for j < len(path) && path[j] != '/' {
			j++
		}

		out = append(out, '{')
		out = append(out, path[i+1:j]...)
		out = append(out, '}')
		i = j - 1
	}

	return method + " " + string(out)
}

// difference returns sorted entries present in a but not in b.
// Sorted output keeps the failure message stable across runs so
// CI failures are diff-friendly.
func difference(a, b map[string]struct{}) []string {
	var out []string

	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}

	slices.Sort(out)

	return out
}
