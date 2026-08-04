package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewAgentConfigurationUsesTheAuthenticatedScopedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", request.Method)
		}
		if request.URL.EscapedPath() != "/v1/organizations/organization%2Ftest/projects/project%20test/agent-configuration-previews" {
			t.Errorf("unexpected path %q", request.URL.EscapedPath())
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Error("request did not carry the configured bearer credential")
		}
		if request.Header.Get("User-Agent") != "terraform-provider-cantora/0.1.0" {
			t.Errorf("unexpected user agent %q", request.Header.Get("User-Agent"))
		}
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(request.Header.Get("X-Request-ID")) {
			t.Errorf("unexpected request identifier %q", request.Header.Get("X-Request-ID"))
		}
		var body struct {
			Desired DesiredAgentConfiguration `json:"desired"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Desired.Key != "aria" {
			t.Errorf("unexpected desired key %q", body.Desired.Key)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(Preview{
			ManifestDigest: strings.Repeat("a", 64),
			Actions:        []string{"createAgentDefinition"},
			Preconditions:  Preconditions{},
			Warnings:       []Warning{},
		})
	}))
	t.Cleanup(server.Close)

	management, err := New(server.URL, "secret", "0.1.0", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	preview, err := management.PreviewAgentConfiguration(
		context.Background(),
		Scope{OrganizationID: "organization/test", ProjectID: "project test"},
		DesiredAgentConfiguration{Intent: "create", Key: "aria"},
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.ManifestDigest != strings.Repeat("a", 64) {
		t.Errorf("unexpected manifest digest %q", preview.ManifestDigest)
	}
}

func TestApplyRetriesAnAmbiguousCommittedResponseWithTheSameIdentity(t *testing.T) {
	var attempts atomic.Int32
	retryStarted := make(chan struct{})
	firstContinued := make(chan struct{})
	requestID := "00000000-0000-8000-8000-000000000001"
	agentDefinitionID := "agentdef_test"
	scope := Scope{OrganizationID: "org_test", ProjectID: "proj_test"}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			RequestID string `json:"requestId"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.RequestID != requestID {
			t.Errorf("retry changed request identity to %q", body.RequestID)
		}
		if attempts.Add(1) == 1 {
			hijacker, ok := response.(http.Hijacker)
			if !ok {
				t.Error("test server does not support connection hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack first response: %v", err)
				return
			}
			_ = connection.Close()
			<-retryStarted
			close(firstContinued)
			return
		}
		close(retryStarted)
		<-firstContinued
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(ApplyResult{
			Revision: ConfigurationRevision{
				ConfigurationRevisionID: "configuration_revision_test",
				RequestID:               requestID,
				OrganizationID:          scope.OrganizationID,
				ProjectID:               scope.ProjectID,
				AgentDefinitionID:       &agentDefinitionID,
				Status:                  "Succeeded",
				Progress: RevisionProgress{
					AgentDefinition: "Succeeded",
					TestAgent:       "Succeeded",
					AgentVersion:    "Succeeded",
					TestRelease:     "Succeeded",
				},
			},
			Configuration: AgentConfiguration{
				OrganizationID:         scope.OrganizationID,
				ProjectID:              scope.ProjectID,
				AgentDefinitionID:      agentDefinitionID,
				Key:                    "aria",
				DisplayName:            "Aria",
				Status:                 "active",
				ConfigurationAuthority: "ApiManaged",
				TestReleaseETag:        `"0"`,
			},
		})
	}))
	t.Cleanup(server.Close)

	management, err := New(server.URL, "secret", "test", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	result, err := management.ApplyAgentConfiguration(
		context.Background(),
		scope,
		requestID,
		DesiredAgentConfiguration{Intent: "create", Key: "aria"},
		Preview{ManifestDigest: strings.Repeat("a", 64)},
		Source{},
	)
	if err != nil {
		t.Fatalf("recover apply: %v", err)
	}
	if attempts.Load() != 2 || result.Revision.RequestID != requestID {
		t.Fatalf("unexpected recovery result %#v after %d attempts", result, attempts.Load())
	}
}

func TestRequestDecodesTypedRefusalWithoutExposingTheCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprintf(response, `{"_tag":"Conflict","resource":"agentDefinition","reason":"credential %s must import it explicitly"}`, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	}))
	t.Cleanup(server.Close)
	management, err := New(server.URL, "credential-that-must-not-appear", "test", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	_, err = management.GetAgentConfiguration(context.Background(), Scope{}, "missing")
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiError.StatusCode != http.StatusConflict || apiError.Tag != "Conflict" || apiError.Resource != "agentDefinition" {
		t.Errorf("unexpected refusal %#v", apiError)
	}
	if strings.Contains(err.Error(), "credential-that-must-not-appear") || apiError.Body == "" {
		t.Error("diagnostic exposed the bearer credential")
	}
}

func TestNewRejectsCredentialsInEndpoint(t *testing.T) {
	_, err := New("https://user:secret@api.cantora.ai", "api-key", "test", nil)
	if err == nil || !strings.Contains(err.Error(), "must not contain credentials") {
		t.Fatalf("expected credential-bearing endpoint refusal, got %v", err)
	}
}

func TestRequestUsesTheConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.Timeout = time.Millisecond
	management, err := New(server.URL, "secret", "test", httpClient)
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	_, err = management.GetAgentConfiguration(context.Background(), Scope{}, "timeout")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func TestRequestRejectsAnOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", 1_048_577)))
	}))
	t.Cleanup(server.Close)
	management, err := New(server.URL, "secret", "test", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	_, err = management.GetAgentConfiguration(context.Background(), Scope{}, "too-large")
	if err == nil || !strings.Contains(err.Error(), "exceeded 1048576 bytes") {
		t.Fatalf("expected bounded-response error, got %v", err)
	}
}

func TestPreviewRejectsAnInvalidResponseValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(Preview{ManifestDigest: "not-a-digest"})
	}))
	t.Cleanup(server.Close)
	management, err := New(server.URL, "secret", "test", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	_, err = management.PreviewAgentConfiguration(context.Background(), Scope{}, DesiredAgentConfiguration{})
	if err == nil || !strings.Contains(err.Error(), "invalid SHA-256 manifest digest") {
		t.Fatalf("expected response validation failure, got %v", err)
	}
}

func TestRequestHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	management, err := New(server.URL, "secret", "test", server.Client())
	if err != nil {
		t.Fatalf("configure client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = management.GetAgentConfiguration(ctx, Scope{}, "cancelled")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
