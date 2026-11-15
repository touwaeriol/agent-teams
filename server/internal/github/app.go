package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
)

// AppSetting is the at_settings key holding the platform App (same as v1).
const AppSetting = "github_app"

// ErrNoApp means the administrator has not created the platform App yet.
var ErrNoApp = errors.New("平台的 GitHub App 还没有创建（部署设置：管理员打开 /setup/github 创建）")

// App is the platform's GitHub App with its secrets opened.
type App struct {
	AppID         int64
	Slug          string
	Name          string
	Owner         string
	HTMLURL       string
	ClientID      string
	ClientSecret  string
	PEM           string
	WebhookSecret string
	Permissions   map[string]string
}

// InstallURL is where users install the App.
func (a *App) InstallURL() string { return Web + "/apps/" + a.Slug + "/installations/new" }

// record is the stored JSON; secrets are Fernet-sealed (v1 format plus two v2 fields).
type record struct {
	AppID         int64             `json:"app_id"`
	Slug          string            `json:"slug"`
	Name          string            `json:"name,omitempty"`
	Owner         string            `json:"owner,omitempty"`
	HTMLURL       string            `json:"html_url,omitempty"`
	ClientID      string            `json:"client_id"`
	ClientSecret  string            `json:"client_secret"`
	PEM           string            `json:"pem"`
	WebhookSecret string            `json:"webhook_secret,omitempty"`
	Permissions   map[string]string `json:"permissions,omitempty"`
}

// Settings is the at_settings access the App store needs.
type Settings interface {
	Setting(ctx context.Context, key string, dest any) (bool, error)
	SaveSetting(ctx context.Context, key string, value any) error
	DeleteSetting(ctx context.Context, key string) error
}

// Apps loads and saves the platform App.
type Apps struct {
	settings Settings
	box      *auth.SecretBox
}

// NewApps returns the App store.
func NewApps(settings Settings, box *auth.SecretBox) *Apps {
	return &Apps{settings: settings, box: box}
}

// Load returns the App, or nil when none is configured.
func (a *Apps) Load(ctx context.Context) (*App, error) {
	var r record
	found, err := a.settings.Setting(ctx, AppSetting, &r)
	if err != nil || !found {
		return nil, err
	}
	app := &App{AppID: r.AppID, Slug: r.Slug, Name: first(r.Name, r.Slug), Owner: r.Owner, HTMLURL: r.HTMLURL,
		ClientID: r.ClientID, Permissions: r.Permissions}
	if app.ClientSecret, err = a.box.Open(r.ClientSecret); err != nil {
		return nil, err
	}
	if app.PEM, err = a.box.Open(r.PEM); err != nil {
		return nil, err
	}
	if r.WebhookSecret != "" {
		app.WebhookSecret, err = a.box.Open(r.WebhookSecret)
	}
	return app, err
}

// Save stores the App GitHub created from the manifest (conversion response).
func (a *Apps) Save(ctx context.Context, conv map[string]any, requested map[string]string) (*App, error) {
	r, err := a.recordOf(conv, requested)
	if err != nil {
		return nil, err
	}
	if err := a.settings.SaveSetting(ctx, AppSetting, r); err != nil {
		return nil, err
	}
	return a.Load(ctx)
}

func (a *Apps) recordOf(conv map[string]any, requested map[string]string) (record, error) {
	id, ok := conv["id"].(float64)
	if !ok || str(conv, "slug") == "" || str(conv, "client_id") == "" || str(conv, "pem") == "" {
		return record{}, fmt.Errorf("GitHub 返回的 App 信息不完整")
	}
	r := record{AppID: int64(id), Slug: str(conv, "slug"), Name: first(str(conv, "name"), str(conv, "slug")),
		HTMLURL: str(conv, "html_url"), ClientID: str(conv, "client_id"), Permissions: requested}
	if owner, ok := conv["owner"].(map[string]any); ok {
		r.Owner = str(owner, "login")
	}
	var err error
	if r.ClientSecret, err = a.box.Seal(str(conv, "client_secret")); err != nil {
		return r, err
	}
	if r.PEM, err = a.box.Seal(str(conv, "pem")); err != nil {
		return r, err
	}
	if secret := str(conv, "webhook_secret"); secret != "" {
		r.WebhookSecret, err = a.box.Seal(secret)
	}
	return r, err
}

// Forget deletes the App.
func (a *Apps) Forget(ctx context.Context) error { return a.settings.DeleteSetting(ctx, AppSetting) }

// Reseal re-encrypts the App's secrets with the current key (atctl admin reseal).
func (a *Apps) Reseal(ctx context.Context) (bool, error) {
	var r record
	found, err := a.settings.Setting(ctx, AppSetting, &r)
	if err != nil || !found {
		return false, err
	}
	for _, field := range []*string{&r.ClientSecret, &r.PEM, &r.WebhookSecret} {
		if *field == "" {
			continue
		}
		if *field, err = a.box.Reseal(*field); err != nil {
			return false, err
		}
	}
	return true, a.settings.SaveSetting(ctx, AppSetting, r)
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
