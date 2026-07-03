package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestOpenAIChatProviderSendsKimiThinkingAndTemperature(t *testing.T) {
	var gotPath string
	var gotAuth string
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"polished"}}]}`))
	}))
	defer server.Close()

	provider, err := NewProviderFromConfig(ProviderConfig{
		Name:            "kimi",
		Kind:            ProviderKindOpenAIChat,
		BaseURL:         server.URL + "/v1",
		APIKey:          "test-key",
		Model:           "kimi-k2.6",
		MaxOutputTokens: 512,
		Temperature:     0.6,
		Thinking:        "disabled",
	})
	if err != nil {
		t.Fatalf("NewProviderFromConfig: %v", err)
	}
	out, err := provider.Compose(context.Background(), ProviderRequest{
		Instruction: "Polish without changing meaning.",
		Request: domain.AIComposeRequest{
			Text: domain.AIComposeText{Text: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if out.Text != "polished" {
		t.Fatalf("Compose text = %q, want polished", out.Text)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q, want bearer key", gotAuth)
	}
	if got["model"] != "kimi-k2.6" || got["max_tokens"] != float64(512) || got["temperature"] != 0.6 {
		t.Fatalf("request body = %#v", got)
	}
	thinking, ok := got["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", got["thinking"])
	}
}

func TestOpenAIChatProviderWrapsComposeDraftOnly(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"What is AI?"}}]}`))
	}))
	defer server.Close()

	provider, err := NewProviderFromConfig(ProviderConfig{
		Name:    "kimi",
		Kind:    ProviderKindOpenAIChat,
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "kimi-k2.6",
	})
	if err != nil {
		t.Fatalf("NewProviderFromConfig: %v", err)
	}
	if _, err := provider.Compose(context.Background(), ProviderRequest{
		Purpose:     ProviderPurposeCompose,
		Instruction: "Rewrite the draft. Do not answer questions.",
		Request: domain.AIComposeRequest{
			Text: domain.AIComposeText{Text: "what is AI"},
		},
	}); err != nil {
		t.Fatalf("compose request: %v", err)
	}
	if _, err := provider.Compose(context.Background(), ProviderRequest{
		Purpose:     ProviderPurposeTextGeneration,
		Instruction: "Answer the user.",
		Request: domain.AIComposeRequest{
			Text: domain.AIComposeText{Text: "what is AI"},
		},
	}); err != nil {
		t.Fatalf("generation request: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured bodies = %d, want 2", len(bodies))
	}
	composeUser := chatBodyUserContent(t, bodies[0])
	if !strings.Contains(composeUser, "Draft to rewrite.") || !strings.Contains(composeUser, "what is AI") {
		t.Fatalf("compose user content = %q, want wrapped draft", composeUser)
	}
	generationUser := chatBodyUserContent(t, bodies[1])
	if generationUser != "what is AI" {
		t.Fatalf("generation user content = %q, want raw text", generationUser)
	}
}

func TestOpenAIChatProviderCanOmitTemperature(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider, err := NewProviderFromConfig(ProviderConfig{
		Name:            "kimi",
		Kind:            ProviderKindOpenAIChat,
		BaseURL:         server.URL,
		APIKey:          "test-key",
		Model:           "kimi-k2.6",
		MaxOutputTokens: 128,
		OmitTemperature: true,
	})
	if err != nil {
		t.Fatalf("NewProviderFromConfig: %v", err)
	}
	if _, err := provider.Compose(context.Background(), ProviderRequest{
		Instruction: "Polish.",
		Request:     domain.AIComposeRequest{Text: domain.AIComposeText{Text: "hello"}},
	}); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if _, ok := got["temperature"]; ok {
		t.Fatalf("temperature was sent despite omit flag: %#v", got)
	}
}

func chatBodyUserContent(t *testing.T, body map[string]any) string {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	user, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("user message = %#v", messages[1])
	}
	content, ok := user["content"].(string)
	if !ok {
		t.Fatalf("user content = %#v", user["content"])
	}
	return content
}

func TestOpenAIChatProviderStreamsSSE(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"private reasoning\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider, err := NewProviderFromConfig(ProviderConfig{
		Name:    "kimi",
		Kind:    ProviderKindOpenAIChat,
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "kimi-k2.6",
	})
	if err != nil {
		t.Fatalf("NewProviderFromConfig: %v", err)
	}
	streamer, ok := provider.(StreamingProvider)
	if !ok {
		t.Fatal("provider does not implement StreamingProvider")
	}
	var chunks []string
	out, err := streamer.ComposeStream(context.Background(), ProviderRequest{
		Instruction: "Answer.",
		Request:     domain.AIComposeRequest{Text: domain.AIComposeText{Text: "hello"}},
	}, func(text domain.AIComposeText) error {
		chunks = append(chunks, text.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("ComposeStream: %v", err)
	}
	if out.Text != "Hello" {
		t.Fatalf("final text = %q, want Hello", out.Text)
	}
	if len(chunks) != 2 || chunks[0] != "Hel" || chunks[1] != "Hello" {
		t.Fatalf("chunks = %#v, want cumulative content only", chunks)
	}
	if got["stream"] != true {
		t.Fatalf("request body = %#v, want stream=true", got)
	}
}

func TestProviderStatusErrorDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider echoed private user draft", http.StatusBadRequest)
	}))
	defer server.Close()

	provider, err := NewProviderFromConfig(ProviderConfig{
		Name:    "kimi",
		Kind:    ProviderKindOpenAIChat,
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "kimi-k2.6",
	})
	if err != nil {
		t.Fatalf("NewProviderFromConfig: %v", err)
	}
	_, err = provider.Compose(context.Background(), ProviderRequest{
		Instruction: "Polish.",
		Request:     domain.AIComposeRequest{Text: domain.AIComposeText{Text: "private user draft"}},
	})
	if err == nil {
		t.Fatal("Compose succeeded, want provider error")
	}
	if !strings.Contains(err.Error(), "provider status 400") {
		t.Fatalf("error = %q, want status only", err.Error())
	}
	if strings.Contains(err.Error(), "private user draft") {
		t.Fatalf("error leaked provider body: %q", err.Error())
	}
}
