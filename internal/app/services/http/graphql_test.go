package http

import (
	"errors"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestGraphQLEndpoint(t *testing.T) {
	t.Parallel()
	src := collection.HTTPSource{
		GraphQL:         &projectfile.GraphQLRequest{ID: "req_1", URL: " {{base}}/graphql "},
		Auth:            projectfile.Auth{Type: projectfile.AuthNone},
		FolderVariables: [][]projectfile.Variable{{{Name: "base", Value: "https://api.test"}}},
	}
	f := newFixture(t, src, nil)
	got, err := GraphQLEndpoint(t.Context(), f.svc, "req_1")
	if err != nil || got != "https://api.test/graphql" {
		t.Errorf("GraphQLEndpoint = %q, %v", got, err)
	}
	p, err := NewGraphQLPoster(t.Context(), f.svc, "req_1")
	if err != nil || p.Endpoint != got {
		t.Fatalf("NewGraphQLPoster = %+v, %v", p, err)
	}
	b, err := p.Build("{ __typename }", "")
	if err != nil || b.Request.Method != "POST" || b.Request.URL != got {
		t.Errorf("Build = %s %s, %v", b.Request.Method, b.Request.URL, err)
	}

	// An undefined variable: the URL's text for the endpoint, an error to post.
	undefined := src
	undefined.GraphQL = &projectfile.GraphQLRequest{ID: "req_1", URL: "{{nope}}/graphql"}
	f = newFixture(t, undefined, nil)
	if got, err := GraphQLEndpoint(t.Context(), f.svc, "req_1"); err != nil || got != "{{nope}}/graphql" {
		t.Errorf("GraphQLEndpoint(undefined) = %q, %v", got, err)
	}
	if _, err := NewGraphQLPoster(t.Context(), f.svc, "req_1"); err == nil {
		t.Error("NewGraphQLPoster(undefined) succeeded")
	}

	// Not a GraphQL request.
	f = newFixture(t, httpSource("https://api.test"), nil)
	if _, err := GraphQLEndpoint(t.Context(), f.svc, "req_1"); !errors.Is(err, &errs.Error{Kind: errs.InvalidArgument, Code: CodeNotGraphQL}) {
		t.Errorf("GraphQLEndpoint(HTTP request) = %v", err)
	}
}
