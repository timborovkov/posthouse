package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	ProviderGoogle     = "google"
	ProviderMicrosoft  = "microsoft"
	googleClientEnv    = "POSTHOUSE_GOOGLE_CLIENT_ID"
	googleSecretEnv    = "POSTHOUSE_GOOGLE_CLIENT_SECRET"
	microsoftClientEnv = "POSTHOUSE_MICROSOFT_CLIENT_ID"
)

// Build-time injected client IDs. Env overrides always win.
var (
	GoogleClientID     string
	GoogleClientSecret string
	MicrosoftClientID  string
)

var (
	GoogleEndpoint = oauth2.Endpoint{
		AuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:      "https://oauth2.googleapis.com/token",
		DeviceAuthURL: "https://oauth2.googleapis.com/device/code",
		AuthStyle:     oauth2.AuthStyleInParams,
	}
	MicrosoftEndpoint = oauth2.Endpoint{
		AuthURL:       "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		DeviceAuthURL: "https://login.microsoftonline.com/common/oauth2/v2.0/devicecode",
		AuthStyle:     oauth2.AuthStyleInParams,
	}
)

type Credentials struct {
	ClientID     string
	ClientSecret string
}

type Config struct {
	Provider     string
	Credentials  Credentials
	Scopes       []string
	Endpoint     oauth2.Endpoint
	HTTPClient   *http.Client
	OpenBrowser  func(string) error
	PrintDevice  func(verificationURL, userCode string)
	Listen       func(context.Context) (net.Listener, error)
	RedirectPath string
}

func CredentialsFor(provider string) (Credentials, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderGoogle:
		id := firstNonEmpty(os.Getenv(googleClientEnv), GoogleClientID)
		if id == "" {
			return Credentials{}, fmt.Errorf("Google client ID is not configured; set %s (publisher has not shipped a verified ID yet)", googleClientEnv)
		}
		return Credentials{ClientID: id, ClientSecret: firstNonEmpty(os.Getenv(googleSecretEnv), GoogleClientSecret)}, nil
	case ProviderMicrosoft:
		id := firstNonEmpty(os.Getenv(microsoftClientEnv), MicrosoftClientID)
		if id == "" {
			return Credentials{}, fmt.Errorf("Microsoft client ID is not configured; set %s (publisher has not shipped a verified ID yet)", microsoftClientEnv)
		}
		return Credentials{ClientID: id}, nil
	default:
		return Credentials{}, fmt.Errorf("unsupported OAuth provider %q", provider)
	}
}

func EndpointFor(provider string) oauth2.Endpoint {
	if strings.EqualFold(provider, ProviderMicrosoft) {
		return MicrosoftEndpoint
	}
	return GoogleEndpoint
}

func (cfg Config) oauth2() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.Credentials.ClientID,
		ClientSecret: cfg.Credentials.ClientSecret,
		Scopes:       cfg.Scopes,
		Endpoint:     cfg.Endpoint,
	}
}

func contextClient(ctx context.Context, client *http.Client) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

func Loopback(ctx context.Context, cfg Config) (string, error) {
	if cfg.RedirectPath == "" {
		cfg.RedirectPath = "/"
	}
	listen := cfg.Listen
	if listen == nil {
		listen = func(context.Context) (net.Listener, error) {
			return net.Listen("tcp", "127.0.0.1:0")
		}
	}
	listener, err := listen(ctx)
	if err != nil {
		return "", fmt.Errorf("listen for OAuth loopback: %w", err)
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.IP == nil || !addr.IP.IsLoopback() {
		return "", fmt.Errorf("OAuth loopback did not bind 127.0.0.1")
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", addr.Port, cfg.RedirectPath)
	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return "", err
	}
	oauthCfg := cfg.oauth2()
	oauthCfg.RedirectURL = redirect
	authURL := oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != cfg.RedirectPath && request.URL.Path != strings.TrimSuffix(cfg.RedirectPath, "/")+"/" && request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		query := request.URL.Query()
		if query.Get("state") != state {
			http.Error(writer, "invalid OAuth state", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("OAuth state mismatch"):
			default:
			}
			return
		}
		if errMsg := query.Get("error"); errMsg != "" {
			http.Error(writer, "authorization denied", http.StatusForbidden)
			select {
			case errCh <- fmt.Errorf("authorization denied: %s", errMsg):
			default:
			}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(writer, "missing code", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("OAuth redirect missing code"):
			default:
			}
			return
		}
		_, _ = io.WriteString(writer, "Posthouse authorized. You can close this window.")
		select {
		case codeCh <- code:
		default:
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := checkBrowserURL(authURL); err != nil {
		return "", err
	}
	open := cfg.OpenBrowser
	if open == nil {
		open = openBrowser
	}
	if err := open(authURL); err != nil {
		return "", fmt.Errorf("open authorization URL: %w", err)
	}
	var code string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case code = <-codeCh:
	}
	token, err := oauthCfg.Exchange(contextClient(ctx, cfg.HTTPClient), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("exchange authorization code: %w", err)
	}
	if token.RefreshToken == "" {
		return "", fmt.Errorf("authorization succeeded without a refresh token; retry with consent")
	}
	return token.RefreshToken, nil
}

func Device(ctx context.Context, cfg Config) (string, error) {
	if strings.EqualFold(cfg.Provider, ProviderGoogle) {
		_, _ = fmt.Fprintln(os.Stderr, "Note: Google Desktop apps often reject device-code. If this fails, run connection auth without --device on a computer with a browser. Microsoft --device is the supported server path.")
	}
	oauthCfg := cfg.oauth2()
	response, err := oauthCfg.DeviceAuth(contextClient(ctx, cfg.HTTPClient))
	if err != nil {
		return "", fmt.Errorf("start device authorization: %w", err)
	}
	print := cfg.PrintDevice
	if print == nil {
		print = func(verificationURL, userCode string) {
			_, _ = fmt.Fprintf(os.Stderr, "To connect this mailbox:\n\n  1. Open %s\n  2. Enter code %s\n\nWaiting for Allow…\n", verificationURL, userCode)
		}
	}
	print(response.VerificationURI, response.UserCode)
	token, err := oauthCfg.DeviceAccessToken(contextClient(ctx, cfg.HTTPClient), response)
	if err != nil {
		return "", fmt.Errorf("complete device authorization: %w", err)
	}
	if token.RefreshToken == "" {
		return "", fmt.Errorf("device authorization succeeded without a refresh token; retry with consent")
	}
	return token.RefreshToken, nil
}

func Refresh(ctx context.Context, cfg Config, refreshToken string) (*oauth2.Token, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("refresh token is missing")
	}
	source := cfg.oauth2().TokenSource(contextClient(ctx, cfg.HTTPClient), &oauth2.Token{RefreshToken: refreshToken})
	token, err := source.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh access token: %w", err)
	}
	return token, nil
}

func HTTPClient(ctx context.Context, cfg Config, refreshToken string) (*http.Client, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("refresh token is missing")
	}
	base := cfg.oauth2().Client(contextClient(ctx, cfg.HTTPClient), &oauth2.Token{RefreshToken: refreshToken})
	return base, nil
}

func Scopes(provider string, mail, calendar bool) []string {
	if !mail && !calendar {
		mail = true
	}
	switch strings.ToLower(provider) {
	case ProviderMicrosoft:
		scopes := []string{"User.Read", "offline_access"}
		if mail {
			scopes = append(scopes, "Mail.Read", "Mail.Send", "Mail.ReadWrite")
		}
		if calendar {
			scopes = append(scopes, "Calendars.Read", "Calendars.ReadWrite")
		}
		return scopes
	default:
		var scopes []string
		if mail {
			// gmail.modify is restricted and covers read, send, drafts, archive, and
			// trash. Do not also request gmail.readonly / gmail.send / gmail.compose —
			// those stack extra restricted/sensitive scopes on the consent screen.
			scopes = append(scopes, "https://www.googleapis.com/auth/gmail.modify")
		}
		if calendar {
			scopes = append(scopes, "https://www.googleapis.com/auth/calendar.readonly", "https://www.googleapis.com/auth/calendar.events")
		}
		return scopes
	}
}

func MailScopes(provider string, withCalendar bool) []string {
	return Scopes(provider, true, withCalendar)
}

func checkBrowserURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("authorization URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("refusing to open non-http authorization URL")
	}
	return nil
}

func openBrowser(rawURL string) error {
	if err := checkBrowserURL(rawURL); err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", rawURL)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		command = exec.Command("xdg-open", rawURL)
	}
	return command.Start()
}

func randomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
