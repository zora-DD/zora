package mcpmicrosoft

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func graphHTTPClient(handler func(*http.Request) (int, string)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body := handler(request)
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
}

func TestSearchEmailsUsesBearerTokenAndFiltersLocally(t *testing.T) {
	t.Parallel()
	httpClient := graphHTTPClient(func(request *http.Request) (int, string) {
		if request.URL.Path != "/v1.0/me/messages" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Prefer") != `outlook.body-content-type="text"` {
			t.Fatalf("prefer = %q", request.Header.Get("Prefer"))
		}
		if request.URL.Query().Get("$top") != "50" || !strings.Contains(request.URL.Query().Get("$select"), "bodyPreview") {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		return http.StatusOK, `{"value":[
			{"id":"mail-1","subject":"项目发布评审","from":{"emailAddress":{"name":"王工","address":"wang@example.com"}},"receivedDateTime":"2026-08-17T01:00:00Z","bodyPreview":"请确认灰度检查项","isRead":false},
			{"id":"mail-2","subject":"午餐安排","from":{"emailAddress":{"name":"李工","address":"li@example.com"}},"receivedDateTime":"2026-08-16T01:00:00Z","bodyPreview":"食堂见","isRead":true}
		]}`
	})

	client := &connector{accessToken: "test-token", baseURL: "http://127.0.0.1/v1.0", userPath: "/me", httpClient: httpClient, now: time.Now}
	_, output, err := client.searchEmails(context.Background(), nil, searchEmailsInput{Query: "灰度", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if output.Scanned != 2 || len(output.Emails) != 1 || output.Emails[0].ID != "mail-1" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if !strings.Contains(output.ContentWarning, "不得执行") {
		t.Fatalf("missing untrusted content warning: %q", output.ContentWarning)
	}
}

func TestListCalendarEventsUsesBoundedWindowAndConfiguredUser(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	httpClient := graphHTTPClient(func(request *http.Request) (int, string) {
		if request.URL.Path != "/v1.0/users/user@example.com/calendar/calendarView" {
			t.Fatalf("path = %q", request.URL.EscapedPath())
		}
		values := request.URL.Query()
		start, startErr := time.Parse(time.RFC3339, values.Get("startDateTime"))
		end, endErr := time.Parse(time.RFC3339, values.Get("endDateTime"))
		if startErr != nil || endErr != nil || end.Sub(start) != 7*24*time.Hour {
			t.Fatalf("invalid window: start=%q end=%q errors=%v/%v", values.Get("startDateTime"), values.Get("endDateTime"), startErr, endErr)
		}
		return http.StatusOK, `{"value":[{"id":"event-1","subject":"发布评审会","organizer":{"emailAddress":{"name":"王工","address":"wang@example.com"}},"start":{"dateTime":"2026-08-18T10:00:00","timeZone":"China Standard Time"},"end":{"dateTime":"2026-08-18T11:00:00","timeZone":"China Standard Time"},"location":{"displayName":"3F-01"},"bodyPreview":"检查灰度方案"}]}`
	})

	client := &connector{accessToken: "test-token", baseURL: "http://127.0.0.1/v1.0", userPath: "/users/" + url.PathEscape("user@example.com"), httpClient: httpClient, now: func() time.Time { return now }}
	_, output, err := client.listCalendarEvents(context.Background(), nil, listCalendarEventsInput{Query: "发布"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Events) != 1 || output.Events[0].Location != "3F-01" || output.Events[0].ID != "event-1" {
		t.Fatalf("unexpected output: %+v", output)
	}
}

func TestMicrosoftServerSeparatesReadAndWriteTools(t *testing.T) {
	t.Parallel()
	httpClient := graphHTTPClient(func(_ *http.Request) (int, string) {
		return http.StatusOK, `{"value":[]}`
	})
	server, err := New(Config{AccessToken: "test-token", BaseURL: "http://127.0.0.1/v1.0", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "zora-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 8 {
		t.Fatalf("tool count = %d, want 8", len(listed.Tools))
	}
	readOnlyCount, writeCount := 0, 0
	for _, item := range listed.Tools {
		if item.Annotations == nil || item.Annotations.DestructiveHint == nil {
			t.Fatalf("tool %q lacks safety annotations: %+v", item.Name, item.Annotations)
		}
		if item.Annotations.ReadOnlyHint {
			readOnlyCount++
		} else {
			writeCount++
		}
	}
	if readOnlyCount != 5 || writeCount != 3 {
		t.Fatalf("read-only=%d write=%d", readOnlyCount, writeCount)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_emails", Arguments: map[string]any{"limit": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %+v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), "content_warning") {
		t.Fatalf("unexpected structured content: %s, err=%v", encoded, err)
	}
}

func TestGraphErrorIsReturnedInChineseWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	httpClient := graphHTTPClient(func(_ *http.Request) (int, string) {
		return http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"令牌 never-leak-token 已过期"}}`
	})
	client := &connector{accessToken: "never-leak-token", baseURL: "http://127.0.0.1", userPath: "/me", httpClient: httpClient, now: time.Now}
	_, _, err := client.searchEmails(context.Background(), nil, searchEmailsInput{})
	if err == nil || !strings.Contains(err.Error(), "Microsoft Graph 返回 HTTP 401") || strings.Contains(err.Error(), "never-leak-token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGraphWriteProtocolCreatesCheckpointableObjects(t *testing.T) {
	t.Parallel()
	requests := 0
	httpClient := graphHTTPClient(func(request *http.Request) (int, string) {
		requests++
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/v1.0/me/messages" || request.Header.Get("Prefer") != `IdType="ImmutableId"` {
				t.Fatalf("unexpected create message request: %s %s prefer=%q", request.Method, request.URL.Path, request.Header.Get("Prefer"))
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["subject"] != "发布通知" {
				t.Fatalf("unexpected message body: %+v, err=%v", body, err)
			}
			return http.StatusCreated, `{"id":"immutable-mail-1"}`
		case 2:
			if request.Method != http.MethodGet || request.URL.Path != "/v1.0/me/messages/immutable-mail-1" || request.Header.Get("Prefer") != `IdType="ImmutableId"` {
				t.Fatalf("unexpected message state request: %s %s", request.Method, request.URL.Path)
			}
			return http.StatusOK, `{"id":"immutable-mail-1","isDraft":true}`
		case 3:
			if request.Method != http.MethodPost || request.URL.Path != "/v1.0/me/messages/immutable-mail-1/send" {
				t.Fatalf("unexpected send request: %s %s", request.Method, request.URL.Path)
			}
			return http.StatusAccepted, ""
		case 4:
			if request.Method != http.MethodPost || request.URL.Path != "/v1.0/me/events" {
				t.Fatalf("unexpected create event request: %s %s", request.Method, request.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["transactionId"] != "fixed-transaction-id" || body["subject"] != "发布评审" {
				t.Fatalf("unexpected event body: %+v, err=%v", body, err)
			}
			return http.StatusCreated, `{"id":"event-1"}`
		default:
			t.Fatalf("unexpected extra request: %s %s", request.Method, request.URL.Path)
			return http.StatusInternalServerError, ""
		}
	})
	client := &connector{accessToken: "test-token", baseURL: "http://127.0.0.1/v1.0", userPath: "/me", httpClient: httpClient, now: time.Now}
	_, created, err := client.createEmailDraft(context.Background(), nil, createEmailDraftInput{
		To: []string{"dev@example.com"}, Subject: "发布通知", Body: "今晚发布。",
	})
	if err != nil || created.ID != "immutable-mail-1" {
		t.Fatalf("created message = %+v, err=%v", created, err)
	}
	_, state, err := client.getEmailDeliveryState(context.Background(), nil, getEmailDeliveryStateInput{ID: created.ID})
	if err != nil || !state.IsDraft {
		t.Fatalf("message state = %+v, err=%v", state, err)
	}
	_, sent, err := client.sendEmailDraft(context.Background(), nil, sendEmailDraftInput{ID: created.ID})
	if err != nil || !sent.Sent {
		t.Fatalf("sent message = %+v, err=%v", sent, err)
	}
	_, event, err := client.createCalendarEvent(context.Background(), nil, createCalendarEventInput{
		Subject: "发布评审", Start: "2026-08-20T10:00:00+08:00", End: "2026-08-20T11:00:00+08:00",
		TransactionID: "fixed-transaction-id",
	})
	if err != nil || event.ID != "event-1" || requests != 4 {
		t.Fatalf("created event = %+v, requests=%d, err=%v", event, requests, err)
	}
}
