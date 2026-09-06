package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	tea "github.com/charmbracelet/bubbletea"
)

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

type session struct {
	config    config
	path      string
	oidc      *ssooidc.Client
	sso       *sso.Client
	program   *tea.Program
	recovered bool
	secrets   []string
}

type authNotice string

func (s *session) notice(text string) { s.program.Send(authNotice(text)) }

func (s *session) rememberSecrets() {
	s.secrets = append(s.secrets, s.config.ClientID, s.config.ClientSecret, s.config.AccessToken, s.config.RefreshToken)
}

type apiError interface {
	error
	ErrorCode() string
	ErrorMessage() string
}

func errorCode(err error) string {
	if api, ok := errors.AsType[apiError](err); ok {
		return api.ErrorCode()
	}
	return ""
}

func (s *session) operationError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return errCancelled
	}
	message := err.Error()
	if api, ok := errors.AsType[apiError](err); ok {
		message = api.ErrorCode() + ": " + api.ErrorMessage()
	}
	for _, secret := range s.secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return fmt.Errorf("%s: %s", operation, terminalText(message))
}

func (s *session) resetAuth() error {
	s.config.clearAuth()
	return saveConfig(s.path, s.config)
}

func (s *session) authenticate(ctx context.Context, force bool) error {
	s.rememberSecrets()
	c := &s.config
	// A registration without any token is valid after interrupted authorization.
	noTokens := c.AccessToken == "" && c.RefreshToken == "" && c.AccessTokenExpiresAt == ""
	_, expiryErr := time.Parse(time.RFC3339, c.AccessTokenExpiresAt)
	consistent := noTokens || (c.AccessToken != "" && expiryErr == nil)
	if !c.validClient() || !consistent {
		if err := s.resetAuth(); err != nil {
			return err
		}
	}
	if !force && c.validClient() && expiresAfter(c.AccessTokenExpiresAt, time.Now().Add(5*time.Minute)) {
		return nil
	}
	if c.validClient() && c.RefreshToken != "" {
		s.notice("Refreshing IAM Identity Center session…")
		out, err := s.oidc.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId: aws.String(c.ClientID), ClientSecret: aws.String(c.ClientSecret),
			GrantType: aws.String("refresh_token"), RefreshToken: aws.String(c.RefreshToken),
		})
		if err == nil {
			return s.storeToken(out)
		}
		switch errorCode(err) {
		case "InvalidGrantException", "InvalidClientException", "UnauthorizedClientException", "ExpiredTokenException", "AccessDeniedException", "invalid_grant", "invalid_client", "unauthorized_client", "expired_token", "access_denied":
			// Rejected refresh state is recoverable; exhausted transport/server
			// retries are operational errors and must remain visible.
		default:
			return s.operationError("refresh access token", err)
		}
		if err := s.resetAuth(); err != nil {
			return err
		}
	} else if !noTokens || force {
		if err := s.resetAuth(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return errCancelled
	}
	if !c.validClient() {
		if err := s.register(ctx); err != nil {
			return err
		}
	}
	return s.authorize(ctx)
}

func (s *session) register(ctx context.Context) error {
	s.notice("Registering IAM Identity Center client…")
	out, err := s.oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("awsx"), ClientType: aws.String("public"),
		Scopes: []string{"sso:account:access"}, GrantTypes: []string{deviceGrant, "refresh_token"},
	})
	if err != nil {
		return s.operationError("register client", err)
	}
	s.config.ClientID = aws.ToString(out.ClientId)
	s.config.ClientSecret = aws.ToString(out.ClientSecret)
	s.config.ClientExpiresAt = time.Unix(out.ClientSecretExpiresAt, 0).UTC().Format(time.RFC3339)
	s.rememberSecrets()
	if !s.config.validClient() {
		return errors.New("register client: AWS returned incomplete or expired registration")
	}
	return saveConfig(s.path, s.config)
}

func (s *session) storeToken(out *ssooidc.CreateTokenOutput) error {
	if aws.ToString(out.AccessToken) == "" || out.ExpiresIn <= 0 {
		return errors.New("create token: AWS returned an incomplete token")
	}
	s.config.AccessToken = aws.ToString(out.AccessToken)
	if aws.ToString(out.RefreshToken) != "" {
		s.config.RefreshToken = aws.ToString(out.RefreshToken)
	}
	s.config.AccessTokenExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	s.rememberSecrets()
	return saveConfig(s.path, s.config)
}

func (s *session) authorize(ctx context.Context) error {
	s.notice("Starting device authorization…")
	input := func() *ssooidc.StartDeviceAuthorizationInput {
		return &ssooidc.StartDeviceAuthorizationInput{ClientId: aws.String(s.config.ClientID), ClientSecret: aws.String(s.config.ClientSecret), StartUrl: aws.String(s.config.StartURL)}
	}
	out, err := s.oidc.StartDeviceAuthorization(ctx, input())
	// A cached registration may have been revoked before its recorded expiry.
	if code := errorCode(err); code == "InvalidClientException" || code == "UnauthorizedClientException" || code == "invalid_client" || code == "unauthorized_client" {
		if err = s.resetAuth(); err != nil {
			return err
		}
		if err = s.register(ctx); err != nil {
			return err
		}
		out, err = s.oidc.StartDeviceAuthorization(ctx, input())
	}
	if err != nil {
		return s.operationError("start device authorization", err)
	}
	verificationURL, userCode := aws.ToString(out.VerificationUri), aws.ToString(out.UserCode)
	if !validStartURL(verificationURL) || userCode == "" || aws.ToString(out.DeviceCode) == "" || out.ExpiresIn <= 0 {
		return errors.New("start device authorization: AWS returned incomplete authorization details")
	}
	s.secrets = append(s.secrets, aws.ToString(out.DeviceCode))
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(out.ExpiresIn)*time.Second)
	defer cancel()
	notice := "Authorize AWSX in your browser\nURL: " + terminalText(verificationURL) + "\nCode: " + terminalText(userCode)
	s.notice(notice + "\nOpening browser; waiting for authorization…")
	openURL := aws.ToString(out.VerificationUriComplete)
	if !validStartURL(openURL) {
		openURL = verificationURL
	}
	// Bound the browser helper separately so a stuck helper cannot stop polling.
	openCtx, stopOpen := context.WithTimeout(pollCtx, 5*time.Second)
	openErr := exec.CommandContext(openCtx, "open", openURL).Run()
	stopOpen()
	if openErr != nil {
		s.notice(notice + "\nCould not open browser. Open the URL manually; waiting for authorization…")
	}
	interval := time.Duration(out.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return errCancelled
			}
			return errors.New("device authorization expired; run awsx login again")
		case <-timer.C:
		}
		token, err := s.oidc.CreateToken(pollCtx, &ssooidc.CreateTokenInput{
			ClientId: aws.String(s.config.ClientID), ClientSecret: aws.String(s.config.ClientSecret),
			GrantType: aws.String(deviceGrant), DeviceCode: out.DeviceCode,
		})
		if err == nil {
			if ctx.Err() != nil {
				return errCancelled
			}
			return s.storeToken(token)
		}
		switch errorCode(err) {
		case "AuthorizationPendingException", "authorization_pending":
		case "SlowDownException", "slow_down":
			interval += 5 * time.Second
		default:
			return s.operationError("poll device authorization", err)
		}
	}
}

// One recovery budget is shared by all SSO operations in this login. A second
// rejection is returned to the user, never an unbounded authentication loop.
func ssoCall[T any](ctx context.Context, s *session, operation string, call func() (T, error)) (T, error) {
	out, err := call()
	code := errorCode(err)
	if err != nil && !s.recovered && (code == "UnauthorizedException" || code == "ExpiredTokenException") {
		s.recovered = true
		if authErr := s.authenticate(ctx, true); authErr != nil {
			return out, authErr
		}
		out, err = call()
	}
	if err != nil {
		return out, s.operationError(operation, err)
	}
	return out, nil
}

func (s *session) accounts(ctx context.Context) ([]ssotypes.AccountInfo, error) {
	var accounts []ssotypes.AccountInfo
	var next *string
	for {
		out, err := ssoCall(ctx, s, "list accounts", func() (*sso.ListAccountsOutput, error) {
			return s.sso.ListAccounts(ctx, &sso.ListAccountsInput{AccessToken: aws.String(s.config.AccessToken), NextToken: next})
		})
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, out.AccountList...)
		next = out.NextToken
		if aws.ToString(next) == "" {
			break
		}
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := strings.ToLower(aws.ToString(accounts[i].AccountName)), strings.ToLower(aws.ToString(accounts[j].AccountName))
		if a == b {
			return aws.ToString(accounts[i].AccountId) < aws.ToString(accounts[j].AccountId)
		}
		return a < b
	})
	return accounts, nil
}

func (s *session) roles(ctx context.Context, accountID string) ([]ssotypes.RoleInfo, error) {
	var roles []ssotypes.RoleInfo
	var next *string
	for {
		out, err := ssoCall(ctx, s, "list account roles", func() (*sso.ListAccountRolesOutput, error) {
			return s.sso.ListAccountRoles(ctx, &sso.ListAccountRolesInput{AccessToken: aws.String(s.config.AccessToken), AccountId: aws.String(accountID), NextToken: next})
		})
		if err != nil {
			return nil, err
		}
		roles = append(roles, out.RoleList...)
		next = out.NextToken
		if aws.ToString(next) == "" {
			break
		}
	}
	sort.SliceStable(roles, func(i, j int) bool {
		return strings.ToLower(aws.ToString(roles[i].RoleName)) < strings.ToLower(aws.ToString(roles[j].RoleName))
	})
	return roles, nil
}

func (s *session) credentials(ctx context.Context, accountID, role string) (*ssotypes.RoleCredentials, error) {
	out, err := ssoCall(ctx, s, "get role credentials", func() (*sso.GetRoleCredentialsOutput, error) {
		return s.sso.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{AccessToken: aws.String(s.config.AccessToken), AccountId: aws.String(accountID), RoleName: aws.String(role)})
	})
	if err != nil {
		return nil, err
	}
	c := out.RoleCredentials
	if c == nil || aws.ToString(c.AccessKeyId) == "" || aws.ToString(c.SecretAccessKey) == "" || aws.ToString(c.SessionToken) == "" || !time.UnixMilli(c.Expiration).After(time.Now()) {
		return nil, errors.New("get role credentials: AWS returned incomplete or expired credentials")
	}
	return c, nil
}
