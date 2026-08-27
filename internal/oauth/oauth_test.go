package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCredentialsForRequireEnv(t *testing.T) {
	t.Setenv(googleClientEnv, "")
	t.Setenv(microsoftClientEnv, "")
	GoogleClientID, MicrosoftClientID = "", ""
	if _, err := CredentialsFor(ProviderGoogle); err == nil || !strings.Contains(err.Error(), googleClientEnv) {
		t.Fatalf("missing Google ID error = %v", err)
	}
	if _, err := CredentialsFor(ProviderMicrosoft); err == nil || !strings.Contains(err.Error(), microsoftClientEnv) {
		t.Fatalf("missing Microsoft ID error = %v", err)
	}
}

func TestCredentialsForUseBuildTimeIDs(t *testing.T) {
	t.Setenv(googleClientEnv, "")
	t.Setenv(googleSecretEnv, "")
	t.Setenv(microsoftClientEnv, "")
	GoogleClientID, GoogleClientSecret, MicrosoftClientID = "ldflag-google", "ldflag-secret", "ldflag-ms"
	defer func() { GoogleClientID, GoogleClientSecret, MicrosoftClientID = "", "", "" }()
	google, err := CredentialsFor(ProviderGoogle)
	if err != nil || google.ClientID != "ldflag-google" || google.ClientSecret != "ldflag-secret" {
		t.Fatalf("Google ldflag credentials = %#v, %v", google, err)
	}
	microsoft, err := CredentialsFor(ProviderMicrosoft)
	if err != nil || microsoft.ClientID != "ldflag-ms" {
		t.Fatalf("Microsoft ldflag credentials = %#v, %v", microsoft, err)
	}
	t.Setenv(googleClientEnv, "env-google")
	t.Setenv(googleSecretEnv, "env-secret")
	google, err = CredentialsFor(ProviderGoogle)
	if err != nil || google.ClientID != "env-google" || google.ClientSecret != "env-secret" {
		t.Fatalf("env override credentials = %#v, %v", google, err)
	}
}

func TestLoopbackStoresRefreshToken(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		gotForm, _ = url.ParseQuery(string(body))
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Provider:     ProviderGoogle,
		Credentials:  Credentials{ClientID: "desktop.apps.googleusercontent.com"},
		Scopes:       MailScopes(ProviderGoogle, false),
		Endpoint:     GoogleEndpoint,
		HTTPClient:   server.Client(),
		Listen:       func(context.Context) (net.Listener, error) { return ln, nil },
		RedirectPath: "/",
	}
	cfg.Endpoint.TokenURL = server.URL
	cfg.OpenBrowser = func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		redirect := parsed.Query().Get("redirect_uri")
		go func() {
			time.Sleep(20 * time.Millisecond)
			resp, err := http.Get(redirect + "?code=auth-code&state=" + parsed.Query().Get("state"))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	token, err := Loopback(context.Background(), cfg)
	if err != nil || token != "refresh-1" {
		t.Fatalf("Loopback = %q, %v", token, err)
	}
	if gotForm.Get("code") != "auth-code" || gotForm.Get("code_verifier") == "" || gotForm.Get("client_secret") != "" {
		t.Fatalf("token form = %v", gotForm)
	}
}

func TestDeviceStoresRefreshToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"device_code": "device-1", "user_code": "ABCD-EFGH", "verification_uri": "https://example.test/link",
			"expires_in": 600, "interval": 0,
		})
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "refresh_token": "refresh-ms", "token_type": "Bearer", "expires_in": 3600})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var printedURL, printedCode string
	cfg := Config{
		Provider:    ProviderMicrosoft,
		Credentials: Credentials{ClientID: "public-client"},
		Scopes:      MailScopes(ProviderMicrosoft, false),
		Endpoint:    MicrosoftEndpoint,
		HTTPClient:  server.Client(),
		PrintDevice: func(verificationURL, userCode string) { printedURL, printedCode = verificationURL, userCode },
	}
	cfg.Endpoint.DeviceAuthURL = server.URL + "/device"
	cfg.Endpoint.TokenURL = server.URL + "/token"
	token, err := Device(context.Background(), cfg)
	if err != nil || token != "refresh-ms" {
		t.Fatalf("Device = %q, %v", token, err)
	}
	if printedURL != "https://example.test/link" || printedCode != "ABCD-EFGH" {
		t.Fatalf("device prompt = %q %q", printedURL, printedCode)
	}
}

func TestGmailMailScopesAreSingleRestrictedModify(t *testing.T) {
	scopes := Scopes(ProviderGoogle, true, false)
	if len(scopes) != 1 || scopes[0] != "https://www.googleapis.com/auth/gmail.modify" {
		t.Fatalf("gmail mail scopes = %#v", scopes)
	}
	withCalendar := Scopes(ProviderGoogle, true, true)
	if len(withCalendar) != 3 || withCalendar[0] != "https://www.googleapis.com/auth/gmail.modify" {
		t.Fatalf("gmail mail+calendar scopes = %#v", withCalendar)
	}
	calendarOnly := Scopes(ProviderGoogle, false, true)
	if len(calendarOnly) != 3 || calendarOnly[0] != "https://www.googleapis.com/auth/userinfo.email" {
		t.Fatalf("gmail calendar-only scopes = %#v", calendarOnly)
	}
	for _, scope := range calendarOnly {
		if scope == "https://www.googleapis.com/auth/gmail.modify" {
			t.Fatal("calendar-only Google scopes requested gmail.modify")
		}
	}
}

func TestCheckBrowserURLRejectsNonHTTP(t *testing.T) {
	if err := checkBrowserURL("javascript:alert(1)"); err == nil {
		t.Fatal("expected non-http URL to be rejected")
	}
	if err := checkBrowserURL("https://accounts.google.com/o/oauth2/v2/auth"); err != nil {
		t.Fatal(err)
	}
}

func TestLoopbackRejectsNonLoopbackBind(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	cfg := Config{
		Provider:     ProviderGoogle,
		Credentials:  Credentials{ClientID: "desktop.apps.googleusercontent.com"},
		Listen:       func(context.Context) (net.Listener, error) { return ln, nil },
		RedirectPath: "/",
		OpenBrowser:  func(string) error { return fmt.Errorf("should not open") },
	}
	_, err = Loopback(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("Loopback non-loopback = %v", err)
	}
}

func TestHTTPClientPersistsRotatedRefreshToken(t *testing.T) {
	var persisted string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"access_token": "graph-access", "refresh_token": "refresh-rotated", "token_type": "Bearer", "expires_in": 3600,
			})
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := Config{
		Credentials:    Credentials{ClientID: "public-client"},
		Endpoint:       MicrosoftEndpoint,
		HTTPClient:     server.Client(),
		PersistRefresh: func(next string) error { persisted = next; return nil },
	}
	cfg.Endpoint.TokenURL = server.URL + "/token"
	client, err := HTTPClient(context.Background(), cfg, "refresh-original")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(server.URL + "/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if persisted != "refresh-rotated" {
		t.Fatal("HTTPClient did not persist the rotated refresh token")
	}
}

func TestRefreshDoesNotLogToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.secret-access", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	cfg := Config{Credentials: Credentials{ClientID: "desktop"}, Endpoint: GoogleEndpoint, HTTPClient: server.Client()}
	cfg.Endpoint.TokenURL = server.URL
	token, err := Refresh(context.Background(), cfg, "refresh-1")
	if err != nil || token.AccessToken != "ya29.secret-access" {
		t.Fatalf("Refresh = %#v, %v", token, err)
	}
	if fmt.Sprint(token) == "ya29.secret-access" {
		t.Fatal("token stringer leaked access token unexpectedly in assertion setup")
	}
}
