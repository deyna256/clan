package management

import (
	"context"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codexoauth"
)

type accountView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	State     string     `json:"state" enum:"connected,refreshing,temporarily_unavailable,needs_sign_in,disabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RetryAt   *time.Time `json:"retry_at,omitempty"`
}

type accountsInput struct {
	ListInput
	State string `query:"state" enum:"connected,refreshing,temporarily_unavailable,needs_sign_in,disabled"`
}

type accountsOutput struct {
	Body page[accountView]
}

type accountOutput struct {
	Body accountView
}

func (s *service) listAccounts(ctx context.Context, input *accountsInput) (*accountsOutput, error) {
	statuses, err := s.oauth.List(ctx)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	items := make([]accountView, 0, len(statuses))
	for _, status := range statuses {
		if !matches(input.Q, string(status.Identity.ID), status.Identity.Name) {
			continue
		}
		if input.State != "" && input.State != string(status.State) {
			continue
		}
		items = append(items, accountResponse(status))
	}
	return &accountsOutput{Body: paginate(items, input.ListInput)}, nil
}

func (s *service) getAccount(ctx context.Context, input *resourceInput) (*accountOutput, error) {
	status, err := s.oauth.Status(ctx, account.ID(input.ID))
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	return &accountOutput{Body: accountResponse(status)}, nil
}

func accountResponse(status codexoauth.AccountStatus) accountView {
	return accountView{
		ID: string(status.Identity.ID), Name: status.Identity.Name, State: string(status.State),
		ExpiresAt: optionalDate(status.ExpiresAt), RetryAt: optionalDate(status.RetryAt),
	}
}

func optionalDate(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func (s *service) disableAccount(ctx context.Context, input *resourceInput) (*struct{}, error) {
	return nil, s.failure(ctx, s.executor.DisableAccount(ctx, account.ID(input.ID)), true)
}

func (s *service) enableAccount(ctx context.Context, input *resourceInput) (*struct{}, error) {
	return nil, s.failure(ctx, s.executor.EnableAccount(ctx, account.ID(input.ID)), true)
}

func (s *service) deleteAccount(ctx context.Context, input *resourceInput) (*struct{}, error) {
	return nil, s.failure(ctx, s.executor.DeleteAccount(ctx, account.ID(input.ID)), true)
}

type startLoginInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" pattern:"\\S"`
	}
}

type loginOutput struct {
	Body struct {
		LoginID          string    `json:"login_id"`
		AccountID        string    `json:"account_id"`
		AuthorizationURL string    `json:"authorization_url"`
		ExpiresAt        time.Time `json:"expires_at"`
	}
}

type loginStatusOutput struct {
	Body struct {
		LoginID   string     `json:"login_id,omitempty"`
		AccountID string     `json:"account_id,omitempty"`
		State     string     `json:"state" enum:"idle,waiting,exchanging,succeeded,failed,canceled,expired"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
}

type cancelLoginInput struct {
	Body struct {
		LoginID string `json:"login_id" minLength:"1" pattern:"\\S"`
	}
}

func (s *service) startLogin(ctx context.Context, input *startLoginInput) (*loginOutput, error) {
	instructions, err := s.oauth.StartLogin(ctx, input.Body.Name)
	if err != nil {
		return nil, s.failure(ctx, err, true)
	}
	return loginResponse(instructions), nil
}

func (s *service) reconnect(ctx context.Context, input *resourceInput) (*loginOutput, error) {
	instructions, err := s.oauth.StartReconnect(ctx, account.ID(input.ID))
	if err != nil {
		return nil, s.failure(ctx, err, true)
	}
	return loginResponse(instructions), nil
}

func loginResponse(instructions codexoauth.LoginInstructions) *loginOutput {
	output := &loginOutput{}
	output.Body.LoginID = instructions.LoginID
	output.Body.AccountID = string(instructions.AccountID)
	output.Body.AuthorizationURL = instructions.URL
	output.Body.ExpiresAt = instructions.ExpiresAt.UTC()
	return output
}

func (s *service) loginStatus(_ context.Context, _ *struct{}) (*loginStatusOutput, error) {
	status := s.oauth.LoginStatus()
	output := &loginStatusOutput{}
	output.Body.LoginID = status.LoginID
	output.Body.AccountID = string(status.AccountID)
	output.Body.State = string(status.State)
	if output.Body.State == "" {
		output.Body.State = "idle"
	}
	output.Body.ExpiresAt = optionalDate(status.ExpiresAt)
	return output, nil
}

func (s *service) cancelLogin(ctx context.Context, input *cancelLoginInput) (*struct{}, error) {
	return nil, s.failure(ctx, s.oauth.CancelLogin(ctx, input.Body.LoginID), true)
}
