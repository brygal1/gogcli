package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

func newGmailServiceForTest(t *testing.T, h http.HandlerFunc) (*gmail.Service, func()) {
	t.Helper()

	srv := httptest.NewServer(h)
	svc, err := gmail.NewService(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("NewService: %v", err)
	}
	return svc, srv.Close
}

func stubGmailServiceForTest(t *testing.T, svc *gmail.Service) {
	t.Helper()
	origNew := newGmailService
	t.Cleanup(func() { newGmailService = origNew })
	newGmailService = func(context.Context, string) (*gmail.Service, error) { return svc, nil }
}

func newPeopleServiceForContactsTest(t *testing.T) (*people.Service, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/contactGroups"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"contactGroups": []map[string]any{
					{"resourceName": "contactGroups/borrower", "name": "Borrower", "formattedName": "Borrower"},
				},
			})
		case strings.Contains(path, "people:searchContacts"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{
						"person": map[string]any{
							"resourceName": "people/c1",
							"names":        []map[string]any{{"displayName": "Nicole Wallace"}},
							"emailAddresses": []map[string]any{
								{"value": "wallacenk4@gmail.com"},
							},
							"memberships": []map[string]any{
								{"contactGroupMembership": map[string]any{"contactGroupResourceName": "contactGroups/borrower"}},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))

	svc, err := people.NewService(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("NewService: %v", err)
	}
	return svc, srv.Close
}
