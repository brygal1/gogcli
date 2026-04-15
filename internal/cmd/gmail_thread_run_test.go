package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

func TestGmailThreadGetAndAttachments_JSON(t *testing.T) {
	origNew := newGmailService
	origPeople := newPeopleContactsService
	t.Cleanup(func() {
		newGmailService = origNew
		newPeopleContactsService = origPeople
	})

	attachmentData := base64.RawURLEncoding.EncodeToString([]byte("payload"))
	threadResp := map[string]any{
		"id": "t1",
		"messages": []map[string]any{
			{
				"id": "m1",
				"payload": map[string]any{
					"headers": []map[string]any{
						{"name": "From", "value": "a@example.com"},
						{"name": "To", "value": "b@example.com"},
						{"name": "Subject", "value": "Hi"},
						{"name": "Date", "value": "Mon, 1 Jan 2025 00:00:00 +0000"},
					},
					"mimeType": "multipart/mixed",
					"parts": []map[string]any{
						{
							"mimeType": "text/plain",
							"body": map[string]any{
								"data": base64.RawURLEncoding.EncodeToString([]byte("hello")),
							},
						},
						{
							"filename": "note.txt",
							"mimeType": "text/plain",
							"body": map[string]any{
								"attachmentId": "att1",
								"size":         7,
							},
						},
					},
				},
			},
		},
	}
	emptyThreadResp := map[string]any{
		"id":       "empty",
		"messages": []map[string]any{},
	}
	noAttsThreadResp := map[string]any{
		"id": "noatts",
		"messages": []map[string]any{
			{
				"id": "m2",
				"payload": map[string]any{
					"mimeType": "text/plain",
					"body": map[string]any{
						"data": base64.RawURLEncoding.EncodeToString([]byte("hello")),
					},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/gmail/v1")
		switch {
		case r.Method == http.MethodGet && path == "/users/me/threads/t1":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(threadResp)
			return
		case r.Method == http.MethodGet && path == "/users/me/threads/empty":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(emptyThreadResp)
			return
		case r.Method == http.MethodGet && path == "/users/me/threads/noatts":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(noAttsThreadResp)
			return
		case r.Method == http.MethodGet && path == "/users/me/messages/m1/attachments/att1":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": attachmentData,
			})
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer srv.Close()

	svc, err := gmail.NewService(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	newGmailService = func(context.Context, string) (*gmail.Service, error) { return svc, nil }
	peopleSvc, peopleClose := newPeopleServiceForContactsTest(t)
	defer peopleClose()
	newPeopleContactsService = func(context.Context, string) (*people.Service, error) { return peopleSvc, nil }

	outDir := t.TempDir()
	getOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--json", "--account", "a@b.com", "gmail", "thread", "get", "t1", "--download", "--out-dir", outDir}); err != nil {
				t.Fatalf("Execute thread get: %v", err)
			}
		})
	})

	var payload struct {
		Thread                map[string]any                   `json:"thread"`
		MessageHeaderContacts map[string]messageHeaderContacts `json:"messageHeaderContacts"`
		Downloaded            []map[string]any                 `json:"downloaded"`
	}
	if err := json.Unmarshal([]byte(getOut), &payload); err != nil {
		t.Fatalf("decode thread json: %v", err)
	}
	if payload.Thread == nil || len(payload.Downloaded) != 1 {
		t.Fatalf("unexpected thread payload: %#v", payload)
	}
	if len(payload.MessageHeaderContacts["m1"].From) != 1 || payload.MessageHeaderContacts["m1"].From[0].InGoogleContacts {
		t.Fatalf("expected unmatched sender enrichment, got: %#v", payload.MessageHeaderContacts)
	}
	messages, ok := payload.Thread["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected thread messages, got: %#v", payload.Thread["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected thread message map, got: %#v", messages[0])
	}
	nestedHeaderContacts, ok := message["headerContacts"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested headerContacts map, got: %#v", message["headerContacts"])
	}
	nestedFromContacts, ok := nestedHeaderContacts["from"].([]any)
	if !ok || len(nestedFromContacts) != 1 {
		t.Fatalf("expected nested from contacts, got: %#v", nestedHeaderContacts["from"])
	}
	path, ok := payload.Downloaded[0]["path"].(string)
	if !ok || path == "" {
		t.Fatalf("expected download path, got: %#v", payload.Downloaded)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected downloaded file: %v", statErr)
	}

	attachmentsOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--json", "--account", "a@b.com", "gmail", "thread", "attachments", "t1"}); err != nil {
				t.Fatalf("Execute attachments: %v", err)
			}
		})
	})
	var attachments struct {
		ThreadID    string           `json:"threadId"`
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(attachmentsOut), &attachments); err != nil {
		t.Fatalf("decode attachments json: %v", err)
	}
	if attachments.ThreadID != "t1" || len(attachments.Attachments) != 1 {
		t.Fatalf("unexpected attachments payload: %#v", attachments)
	}
	if attachments.Attachments[0]["filename"] != "note.txt" {
		t.Fatalf("unexpected attachment filename: %#v", attachments.Attachments[0])
	}

	attachmentsDownloadOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--json", "--account", "a@b.com", "gmail", "thread", "attachments", "t1", "--download", "--out-dir", outDir}); err != nil {
				t.Fatalf("Execute attachments download: %v", err)
			}
		})
	})
	var attachmentsDownloaded struct {
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(attachmentsDownloadOut), &attachmentsDownloaded); err != nil {
		t.Fatalf("decode attachments download: %v", err)
	}
	if len(attachmentsDownloaded.Attachments) != 1 {
		t.Fatalf("unexpected download attachments: %#v", attachmentsDownloaded.Attachments)
	}
	if _, ok := attachmentsDownloaded.Attachments[0]["path"]; !ok {
		t.Fatalf("expected download path in attachments: %#v", attachmentsDownloaded.Attachments[0])
	}

	plainOutDir := t.TempDir()
	plainDownloadOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--account", "a@b.com", "gmail", "thread", "attachments", "t1", "--download", "--out-dir", plainOutDir}); err != nil {
				t.Fatalf("Execute attachments download plain: %v", err)
			}
		})
	})
	if !strings.Contains(plainDownloadOut, "Saved") {
		t.Fatalf("unexpected download output: %q", plainDownloadOut)
	}

	cachedOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--account", "a@b.com", "gmail", "thread", "attachments", "t1", "--download", "--out-dir", plainOutDir}); err != nil {
				t.Fatalf("Execute attachments cached: %v", err)
			}
		})
	})
	if !strings.Contains(cachedOut, "Cached") {
		t.Fatalf("unexpected cached output: %q", cachedOut)
	}

	// Ensure path is within the requested output dir when downloading attachments.
	if !strings.HasPrefix(path, filepath.Clean(outDir)+string(os.PathSeparator)) {
		t.Fatalf("unexpected download path: %s", path)
	}

	plainOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--account", "a@b.com", "gmail", "thread", "get", "t1"}); err != nil {
				t.Fatalf("Execute thread get plain: %v", err)
			}
		})
	})
	if !strings.Contains(plainOut, "Thread contains") {
		t.Fatalf("unexpected plain output: %q", plainOut)
	}
	if !strings.Contains(plainOut, "From contact: a@example.com [in_google_contacts=false]") {
		t.Fatalf("expected plain contact summary, got: %q", plainOut)
	}

	emptyErr := captureStderr(t, func() {
		if err := Execute([]string{"--account", "a@b.com", "gmail", "thread", "get", "empty"}); err != nil {
			t.Fatalf("Execute empty thread: %v", err)
		}
	})
	if !strings.Contains(emptyErr, "Empty thread") {
		t.Fatalf("unexpected empty thread stderr: %q", emptyErr)
	}

	noAttsOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--account", "a@b.com", "gmail", "thread", "attachments", "noatts"}); err != nil {
				t.Fatalf("Execute no attachments: %v", err)
			}
		})
	})
	if !strings.Contains(noAttsOut, "No attachments found") {
		t.Fatalf("unexpected no attachments output: %q", noAttsOut)
	}

	emptyAttachOut := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			if err := Execute([]string{"--json", "--account", "a@b.com", "gmail", "thread", "attachments", "empty"}); err != nil {
				t.Fatalf("Execute empty attachments json: %v", err)
			}
		})
	})
	if !strings.Contains(emptyAttachOut, "\"attachments\"") {
		t.Fatalf("unexpected empty attachments output: %q", emptyAttachOut)
	}
}
