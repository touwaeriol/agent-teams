package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Remote servers (讨论串 #38, api.md §3.9.1, remote-servers.md): the user keeps servers in their
// settings, a project picks some and grants each to roles. No response ever carries a password,
// private key, passphrase or ciphertext; a credential can only be replaced.

// Test connection limits (remote-servers.md §5.6).
const (
	serverTestTimeout   = 20 * time.Second
	serverTestPerServer = 10 // per minute
	serverTestPerUser   = 30 // per minute
)

var (
	serverNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	usernameRE   = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._@-]{0,63}$`)
)

func mustVault(secrets []string) *credvault.Vault {
	v, err := credvault.New(secrets...)
	if err != nil {
		panic(err)
	}
	return v
}

// ServerView is api.md ServerView: a server without any secret.
type ServerView struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name" doc:"ssh Host alias in sessions: lowercase letters, digits and -"`
	Host               string             `json:"host"`
	Port               int                `json:"port"`
	Username           string             `json:"username"`
	AuthKind           string             `json:"auth_kind" enum:"password,private_key"`
	HasPassphrase      bool               `json:"has_passphrase"`
	KeyFingerprint     string             `json:"key_fingerprint" doc:"private_key: SHA256 fingerprint of its public key; '' for password"`
	HostKeyFP          string             `json:"host_key_fp" doc:"Pinned host key fingerprint; '' until the first successful test connection"`
	HostKeyConfirmedAt *time.Time         `json:"host_key_confirmed_at" nullable:"true"`
	CredUpdatedAt      time.Time          `json:"cred_updated_at"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	Projects           []store.ProjectRef `json:"projects" doc:"Projects using the server"`
}

func serverView(r store.RemoteServer, projects []store.ProjectRef) ServerView {
	if projects == nil {
		projects = []store.ProjectRef{}
	}
	return ServerView{ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, Username: r.Username, AuthKind: r.AuthKind,
		HasPassphrase: r.HasPassphrase, KeyFingerprint: r.KeyFingerprint, HostKeyFP: r.HostKeyFP,
		HostKeyConfirmedAt: r.HostKeyConfirmedAt, CredUpdatedAt: r.CredUpdatedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Projects: projects}
}

// ServerAuth is a credential in a request (write only).
type ServerAuth struct {
	Kind       string `json:"kind" enum:"password,private_key"`
	Password   string `json:"password,omitempty" maxLength:"1024"`
	PrivateKey string `json:"private_key,omitempty" maxLength:"16384" doc:"OpenSSH or PEM private key"`
	Passphrase string `json:"passphrase,omitempty" maxLength:"1024"`
}

func (a ServerAuth) credential() credvault.Credential {
	key := strings.TrimSpace(a.PrivateKey)
	if key != "" {
		key += "\n"
	}
	return credvault.Credential{Password: a.Password, PrivateKey: key, Passphrase: a.Passphrase}
}

type serversOutput struct{ Body []ServerView }

type serverOutput struct{ Body ServerView }

type serverIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type createServerInput struct {
	Body struct {
		Name     string     `json:"name" maxLength:"63"`
		Host     string     `json:"host" maxLength:"253"`
		Port     int        `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Default 22"`
		Username string     `json:"username" maxLength:"64"`
		Auth     ServerAuth `json:"auth"`
	}
}

type updateServerInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Name     *string     `json:"name,omitempty" maxLength:"63"`
		Host     *string     `json:"host,omitempty" maxLength:"253"`
		Port     *int        `json:"port,omitempty" minimum:"1" maximum:"65535"`
		Username *string     `json:"username,omitempty" maxLength:"64"`
		Auth     *ServerAuth `json:"auth,omitempty" doc:"Replaces the credential; required when host, port, username or the auth kind changes"`
	}
}

// ServerTestResult is the answer of POST /api/servers/{id}/test.
type ServerTestResult struct {
	OK             bool   `json:"ok" doc:"The host key matched (or was pinned now) and the credential logged in"`
	Error          string `json:"error,omitempty" enum:"host_key_mismatch,host_key_invalid,auth_failed,address_blocked,resolve_failed,connect_failed,timeout,handshake_failed,credential_unreadable"`
	Message        string `json:"message,omitempty"`
	HostKeyFP      string `json:"host_key_fp,omitempty" doc:"Fingerprint the target showed"`
	HostKeyType    string `json:"host_key_type,omitempty"`
	HostKeyPinned  bool   `json:"host_key_pinned" doc:"This test pinned the host key (first test)"`
	ExpectedHostFP string `json:"expected_host_key_fp,omitempty" doc:"host_key_mismatch: the pinned fingerprint"`
	LatencyMS      int64  `json:"latency_ms"`
}

type serverTestOutput struct{ Body ServerTestResult }

func (s *Server) registerServers(api huma.API) {
	tags := []string{"servers"}
	huma.Register(api, huma.Operation{OperationID: "list-servers", Method: "GET", Path: "/api/servers", Tags: tags,
		Summary: "My remote servers"}, s.listServers)
	huma.Register(api, huma.Operation{OperationID: "create-server", Method: "POST", Path: "/api/servers", Tags: tags,
		Summary: "Add a remote server (credential sealed, never returned)", DefaultStatus: 201}, s.createServer)
	huma.Register(api, huma.Operation{OperationID: "get-server", Method: "GET", Path: "/api/servers/{id}", Tags: tags}, s.getServer)
	huma.Register(api, huma.Operation{OperationID: "update-server", Method: "PATCH", Path: "/api/servers/{id}", Tags: tags,
		Summary: "Change a server or replace its credential"}, s.updateServer)
	huma.Register(api, huma.Operation{OperationID: "delete-server", Method: "DELETE", Path: "/api/servers/{id}", Tags: tags,
		DefaultStatus: 204}, s.deleteServer)
	huma.Register(api, huma.Operation{OperationID: "test-server", Method: "POST", Path: "/api/servers/{id}/test", Tags: tags,
		Summary: "Log in from the console; the first test pins the host key"}, s.testServer)
	huma.Register(api, huma.Operation{OperationID: "reset-server-host-key", Method: "POST", Path: "/api/servers/{id}/host-key/reset", Tags: tags,
		Summary: "Forget the pinned host key (the next test pins the new one)"}, s.resetServerHostKey)
	huma.Register(api, huma.Operation{OperationID: "server-audit", Method: "GET", Path: "/api/servers/{id}/audit", Tags: tags,
		Summary: "Access log of a server"}, s.serverAudit)
	huma.Register(api, huma.Operation{OperationID: "get-project-servers", Method: "GET", Path: "/api/projects/{pid}/servers", Tags: tags,
		Summary: "Servers the project uses, their roles, and the roles that can be granted"}, s.getProjectServers)
	huma.Register(api, huma.Operation{OperationID: "set-project-servers", Method: "PUT", Path: "/api/projects/{pid}/servers", Tags: tags,
		Summary: "Replace the project's servers and role grants"}, s.setProjectServers)
	huma.Register(api, huma.Operation{OperationID: "project-server-audit", Method: "GET", Path: "/api/projects/{pid}/server-audit", Tags: tags,
		Summary: "Access log of the project's servers"}, s.projectServerAudit)
}

func (s *Server) requireVault() error {
	if !s.vault.Ready() {
		return fail(http.StatusServiceUnavailable, "no_secret_key", credvault.ErrNoKey.Error())
	}
	return nil
}

// sshPolicy is the target address policy with the administrator's allowed networks.
func (s *Server) sshPolicy(ctx context.Context) sshdial.Policy {
	var set sshdial.AllowedSetting
	if _, err := s.store.Setting(ctx, sshdial.SettingKey, &set); err != nil {
		s.log.Warn("ssh allowed networks", "err", err)
	}
	allowed, err := sshdial.ParseAllowed(set)
	if err != nil {
		s.log.Warn("ssh allowed networks ignored", "err", err)
		allowed = nil
	}
	return sshdial.Policy{Allowed: allowed}
}

// checkTarget validates a host before saving it (and again before every dial).
func (s *Server) checkTarget(ctx context.Context, host string) error {
	_, err := s.sshPolicy(ctx).Resolve(ctx, host)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sshdial.ErrInvalidHost):
		return invalid([]FieldError{{Path: "body.host", Message: "主机名或 IP 地址不合法"}}, "主机名或 IP 地址不合法")
	case errors.Is(err, sshdial.ErrResolve):
		return failf(http.StatusUnprocessableEntity, "resolve_failed", "主机名解析不到地址：{host}", "host", host)
	case errors.Is(err, sshdial.ErrBlockedAddress):
		return fail(http.StatusUnprocessableEntity, "address_blocked", err.Error())
	}
	return err
}

func checkServerFields(name, username *string, port *int) error {
	var fields []FieldError
	if name != nil && !serverNameRE.MatchString(*name) {
		fields = append(fields, FieldError{Path: "body.name", Message: "名字只能用小写字母、数字和 -，以字母或数字开头，最多 63 个字符"})
	}
	if username != nil && !usernameRE.MatchString(*username) {
		fields = append(fields, FieldError{Path: "body.username", Message: "用户名不合法"})
	}
	if port != nil && (*port < 1 || *port > 65535) {
		fields = append(fields, FieldError{Path: "body.port", Message: "端口要在 1–65535 之间"})
	}
	if len(fields) > 0 {
		return invalid(fields, fields[0].Message)
	}
	return nil
}

func credErr(err error) error {
	if errors.Is(err, credvault.ErrInvalidCredential) {
		msg := strings.TrimPrefix(err.Error(), credvault.ErrInvalidCredential.Error()+"：")
		return invalid([]FieldError{{Path: "body.auth", Message: msg}}, msg)
	}
	return err
}

func serverStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrConflict):
		return fail(http.StatusConflict, "server_name_taken", "已经有同名的服务器了")
	case errors.Is(err, store.ErrCredentialRequired):
		return fail(http.StatusUnprocessableEntity, "credential_required", store.ErrCredentialRequired.Error())
	}
	return storeErr(err, "没有这台服务器")
}

func (s *Server) listServers(ctx context.Context, _ *struct{}) (*serversOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.RemoteServers(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	used, err := s.store.ServerProjects(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ServerView, len(rows))
	for i, r := range rows {
		out[i] = serverView(r, used[r.ID])
	}
	return &serversOutput{Body: out}, nil
}

func (s *Server) oneServerView(ctx context.Context, userID string, r store.RemoteServer) (ServerView, error) {
	used, err := s.store.ServerProjects(ctx, userID)
	return serverView(r, used[r.ID]), err
}

func (s *Server) getServer(ctx context.Context, in *serverIDInput) (*serverOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.store.RemoteServer(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	v, err := s.oneServerView(ctx, p.User.ID, r)
	return &serverOutput{Body: v}, err
}

func (s *Server) createServer(ctx context.Context, in *createServerInput) (*serverOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireVault(); err != nil {
		return nil, err
	}
	b := in.Body
	b.Name, b.Host, b.Username = strings.TrimSpace(b.Name), strings.TrimSpace(b.Host), strings.TrimSpace(b.Username)
	if b.Port == 0 {
		b.Port = 22
	}
	if err := checkServerFields(&b.Name, &b.Username, &b.Port); err != nil {
		return nil, err
	}
	if err := s.checkTarget(ctx, b.Host); err != nil {
		return nil, err
	}
	srv := store.RemoteServer{ID: store.NewID("srv"), UserID: p.User.ID, Name: b.Name, Host: b.Host, Port: b.Port,
		Username: b.Username, AuthKind: b.Auth.Kind}
	cred, err := s.sealServer(srv, b.Auth)
	if err != nil {
		return nil, err
	}
	row, err := s.store.CreateRemoteServer(ctx, srv, *cred)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	s.audit(ctx, p.User.ID, "server_create", "server", row.ID, map[string]any{"name": row.Name, "host": row.Host,
		"port": row.Port, "username": row.Username, "auth_kind": row.AuthKind})
	return &serverOutput{Body: serverView(row, nil)}, nil
}

// sealServer checks and seals a credential for srv as it will be stored (AAD = its final values).
func (s *Server) sealServer(srv store.RemoteServer, auth ServerAuth) (*store.ServerCredential, error) {
	c := auth.credential()
	if auth.Kind != credvault.AuthPrivateKey {
		c.PrivateKey = ""
	}
	aad := credvault.ServerAAD(srv.UserID, srv.ID, srv.AuthKind, srv.Host, srv.Port, srv.Username)
	sealed, info, err := s.vault.SealCredential(aad, srv.AuthKind, c)
	if err != nil {
		return nil, credErr(err)
	}
	return &store.ServerCredential{Sealed: sealed, HasPassphrase: c.Passphrase != "", KeyFingerprint: info.Fingerprint}, nil
}

func (s *Server) updateServer(ctx context.Context, in *updateServerInput) (*serverOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	trim := func(v *string) *string {
		if v == nil {
			return nil
		}
		t := strings.TrimSpace(*v)
		return &t
	}
	b.Name, b.Host, b.Username = trim(b.Name), trim(b.Host), trim(b.Username)
	if err := checkServerFields(b.Name, b.Username, b.Port); err != nil {
		return nil, err
	}
	if b.Host != nil {
		if err := s.checkTarget(ctx, *b.Host); err != nil {
			return nil, err
		}
	}
	patch := store.RemoteServerPatch{Name: b.Name, Host: b.Host, Port: b.Port, Username: b.Username}
	var seal store.SealFunc
	if b.Auth != nil {
		if err := s.requireVault(); err != nil {
			return nil, err
		}
		patch.AuthKind = &b.Auth.Kind
		auth := *b.Auth
		// Sealed inside the update's transaction, for the row as it will be stored.
		seal = func(merged store.RemoteServer) (*store.ServerCredential, error) { return s.sealServer(merged, auth) }
	}
	before, err := s.store.RemoteServer(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	row, revoked, err := s.store.UpdateRemoteServer(ctx, p.User.ID, in.ID, patch, seal)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	s.certsRevoked(ctx, revoked)
	if row.Name != before.Name {
		if ids, err := s.store.ServerProjectIDs(ctx, p.User.ID, row.ID); err == nil && len(ids) > 0 {
			s.ssh.ServersChanged(ctx, p.User.ID, ids)
		}
	}
	detail := map[string]any{"name": row.Name, "host": row.Host, "port": row.Port, "username": row.Username, "auth_kind": row.AuthKind,
		"credential_replaced": b.Auth != nil}
	s.audit(ctx, p.User.ID, "server_update", "server", row.ID, detail)
	v, err := s.oneServerView(ctx, p.User.ID, row)
	return &serverOutput{Body: v}, err
}

func (s *Server) certsRevoked(ctx context.Context, serials []int64) {
	if len(serials) > 0 {
		s.ssh.CertsRevoked(ctx, serials)
	}
}

func (s *Server) deleteServer(ctx context.Context, in *serverIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.store.ServerProjectIDs(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, err
	}
	revoked, err := s.store.DeleteRemoteServer(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	s.certsRevoked(ctx, revoked)
	if len(ids) > 0 {
		s.ssh.ServersChanged(ctx, p.User.ID, ids)
	}
	s.audit(ctx, p.User.ID, "server_delete", "server", in.ID, nil)
	return nil, nil
}

func (s *Server) resetServerHostKey(ctx context.Context, in *serverIDInput) (*serverOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	before, err := s.store.RemoteServer(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	row, err := s.store.SetRemoteServerHostKey(ctx, p.User.ID, in.ID, "", "", nil)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	s.audit(ctx, p.User.ID, "server_host_key_reset", "server", in.ID, map[string]string{"old_fingerprint": before.HostKeyFP})
	v, err := s.oneServerView(ctx, p.User.ID, row)
	return &serverOutput{Body: v}, err
}

// testServer logs in to the server from the console with its stored credential. The first test
// (no pinned host key) pins what the target shows; later tests refuse another key before sending
// the credential. The answer is 200 with ok/error for every outcome of the connection itself.
func (s *Server) testServer(ctx context.Context, in *serverIDInput) (*serverTestOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireVault(); err != nil {
		return nil, err
	}
	sec, err := s.store.RemoteServerSecret(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, serverStoreErr(err)
	}
	if err := s.rateLimit(ctx, "server-test:"+sec.ID, serverTestPerServer, time.Minute); err != nil {
		return nil, err
	}
	if err := s.rateLimit(ctx, "server-test-user:"+p.User.ID, serverTestPerUser, time.Minute); err != nil {
		return nil, err
	}
	res := s.runServerTest(ctx, p.User.ID, sec)
	exit := 0
	if !res.OK {
		exit = 1
	}
	dur := res.LatencyMS
	audit := store.SSHAudit{UserID: p.User.ID, ServerID: sec.ID, ServerName: sec.Name, Kind: store.SSHAuditTest, Employee: "user",
		ExitCode: &exit, DurationMS: &dur, Reason: res.Error, Detail: map[string]any{"host_key_fp": res.HostKeyFP, "pinned": res.HostKeyPinned}}
	if _, err := s.store.InsertSSHAudit(ctx, audit); err != nil {
		s.log.Warn("ssh audit", "err", err)
	}
	return &serverTestOutput{Body: res}, nil
}

func (s *Server) runServerTest(ctx context.Context, userID string, sec store.RemoteServerSecret) ServerTestResult {
	failed := func(code, msg string) ServerTestResult { return ServerTestResult{Error: code, Message: msg} }
	cred, err := s.vault.OpenCredential(credvault.ServerAAD(userID, sec.ID, sec.AuthKind, sec.Host, sec.Port, sec.Username), sec.Sealed)
	if err != nil {
		return failed("credential_unreadable", "保存的凭证解不开（主密钥变过，或数据被改动过），请重新填写凭证")
	}
	if sec.HostKey != "" {
		if ok, _ := s.vault.VerifyMAC(credvault.HostKeyMACData(sec.ID, sec.Host, sec.Port, sec.HostKey), sec.HostKeyMAC); !ok {
			return failed("host_key_invalid", "已固定的主机密钥校验不通过（可能被直接改过数据库），请重置主机密钥后重新测试")
		}
	}
	start := time.Now()
	dial, err := s.sshPolicy(ctx).Dial(ctx, sshdial.Target{Host: sec.Host, Port: sec.Port, Username: sec.Username,
		AuthKind: sec.AuthKind, Cred: cred, HostKey: sec.HostKey}, serverTestTimeout)
	res := ServerTestResult{LatencyMS: time.Since(start).Milliseconds()}
	if dial.Seen != nil {
		res.HostKeyFP, res.HostKeyType = dial.Seen.Fingerprint, dial.Seen.Type
		if sec.HostKey == "" {
			mac, merr := s.vault.MAC(credvault.HostKeyMACData(sec.ID, sec.Host, sec.Port, dial.Seen.Line))
			if merr == nil {
				pinned, perr := s.store.PinRemoteServerHostKey(ctx, userID, sec.ID, sec.Host, sec.Port, dial.Seen.Line, dial.Seen.Fingerprint, mac)
				if perr != nil {
					s.log.Warn("pin host key", "server_id", sec.ID, "err", perr)
				}
				res.HostKeyPinned = pinned
				if pinned {
					s.audit(ctx, userID, "server_host_key_pin", "server", sec.ID, map[string]string{"fingerprint": dial.Seen.Fingerprint})
				}
			}
		}
	}
	if dial.Client != nil {
		_ = dial.Client.Close()
	}
	switch {
	case err == nil:
		res.OK = true
	case errors.Is(err, sshdial.ErrHostKeyMismatch):
		res.Error, res.ExpectedHostFP = "host_key_mismatch", sec.HostKeyFP
		res.Message = "目标服务器的主机密钥和已固定的不一样，没有发送凭证。请核实服务器是否重装或被冒充；确认无误后重置主机密钥再测试"
	case errors.Is(err, sshdial.ErrAuth):
		res.Error, res.Message = "auth_failed", "认证失败：密码或私钥不对，或用户不存在"
	case errors.Is(err, sshdial.ErrBlockedAddress):
		res.Error, res.Message = "address_blocked", err.Error()
	case errors.Is(err, sshdial.ErrResolve), errors.Is(err, sshdial.ErrInvalidHost):
		res.Error, res.Message = "resolve_failed", "主机名解析不到地址："+sec.Host
	case errors.Is(err, sshdial.ErrTimeout):
		res.Error, res.Message = "timeout", "连接超时"
	case errors.Is(err, sshdial.ErrConnect):
		res.Error, res.Message = "connect_failed", "连不上 "+sec.Host+"，请检查主机、端口和防火墙"
	case errors.Is(err, credvault.ErrInvalidCredential):
		res.Error, res.Message = "credential_unreadable", err.Error()
	default:
		res.Error, res.Message = "handshake_failed", "SSH 握手失败："+err.Error()
	}
	return res
}

// ---- project servers ---------------------------------------------------------------------

// ProjectServerView is one server of a project with the roles granted to it.
type ProjectServerView struct {
	Server ServerView `json:"server"`
	Roles  []string   `json:"roles"`
}

// GrantableRoleView is a role of the user and whether it may be granted servers.
type GrantableRoleView struct {
	Role      string `json:"role"`
	Title     string `json:"title"`
	Tier      string `json:"tier" enum:"coordinator,lead,member"`
	Grantable bool   `json:"grantable"`
	Reason    string `json:"reason,omitempty" enum:"coordinator,lead,read_only" doc:"Why it cannot be granted"`
}

// ProjectServersView is GET/PUT /api/projects/{pid}/servers.
type ProjectServersView struct {
	Servers   []ProjectServerView `json:"servers" doc:"Servers the project uses, by name"`
	Available []ServerView        `json:"available" doc:"All of the user's servers (to pick from)"`
	Roles     []GrantableRoleView `json:"roles"`
}

type projectServersOutput struct{ Body ProjectServersView }

type setProjectServersInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		Servers []struct {
			ServerID string   `json:"server_id" maxLength:"64"`
			Roles    []string `json:"roles" maxItems:"100"`
		} `json:"servers" maxItems:"200"`
	}
}

func (s *Server) projectServersView(ctx context.Context, userID, projectID string) (ProjectServersView, error) {
	var v ProjectServersView
	list, err := s.store.ProjectServers(ctx, userID, projectID)
	if err != nil {
		return v, err
	}
	all, err := s.store.RemoteServers(ctx, userID)
	if err != nil {
		return v, err
	}
	used, err := s.store.ServerProjects(ctx, userID)
	if err != nil {
		return v, err
	}
	roles, err := s.store.ProjectServerRoles(ctx, userID, projectID)
	if err != nil {
		return v, err
	}
	v.Servers = make([]ProjectServerView, len(list))
	for i, ps := range list {
		v.Servers[i] = ProjectServerView{Server: serverView(ps.Server, used[ps.Server.ID]), Roles: ps.Roles}
	}
	v.Available = make([]ServerView, len(all))
	for i, r := range all {
		v.Available[i] = serverView(r, used[r.ID])
	}
	v.Roles = make([]GrantableRoleView, len(roles))
	for i, r := range roles {
		v.Roles[i] = GrantableRoleView{Role: r.Role, Title: r.Title, Tier: r.Tier, Grantable: r.Reason == "", Reason: r.Reason}
	}
	return v, nil
}

func (s *Server) getProjectServers(ctx context.Context, in *projectIDInput) (*projectServersOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ProjectOf(ctx, p.User.ID, in.PID); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	v, err := s.projectServersView(ctx, p.User.ID, in.PID)
	return &projectServersOutput{Body: v}, err
}

func (s *Server) setProjectServers(ctx context.Context, in *setProjectServersInput) (*projectServersOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ProjectOf(ctx, p.User.ID, in.PID); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	list := make([]store.ProjectServerSetting, 0, len(in.Body.Servers))
	seen := map[string]bool{}
	for _, ps := range in.Body.Servers {
		if seen[ps.ServerID] {
			return nil, invalid([]FieldError{{Path: "body.servers", Message: "同一台服务器出现了两次"}}, "同一台服务器出现了两次")
		}
		seen[ps.ServerID] = true
		list = append(list, store.ProjectServerSetting{ServerID: ps.ServerID, Roles: ps.Roles})
	}
	before, err := s.store.ProjectServers(ctx, p.User.ID, in.PID)
	if err != nil {
		return nil, err
	}
	revoked, err := s.store.SetProjectServers(ctx, p.User.ID, in.PID, list)
	switch {
	case errors.Is(err, store.ErrRoleNotGrantable):
		return nil, fail(http.StatusUnprocessableEntity, "role_not_grantable", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return nil, fail(http.StatusNotFound, "not_found", "没有这台服务器")
	case err != nil:
		return nil, err
	}
	s.certsRevoked(ctx, revoked)
	v, err := s.projectServersView(ctx, p.User.ID, in.PID)
	if err != nil {
		return nil, err
	}
	if !sameProjectServers(before, v.Servers) {
		s.ssh.ServersChanged(ctx, p.User.ID, []string{in.PID})
		grants := map[string][]string{}
		for _, ps := range v.Servers {
			grants[ps.Server.Name] = ps.Roles
		}
		s.audit(ctx, p.User.ID, "project_servers_set", "project", in.PID, map[string]any{"grants": grants})
	}
	return &projectServersOutput{Body: v}, nil
}

func sameProjectServers(a []store.ProjectServer, b []ProjectServerView) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Server.ID != b[i].Server.ID || a[i].Server.Name != b[i].Server.Name || !slices.Equal(a[i].Roles, b[i].Roles) {
			return false
		}
	}
	return true
}

// ---- audit -------------------------------------------------------------------------------

// SshAuditPage is a page of api.md SshAuditView rows, newest first.
type SshAuditPage struct {
	Items      []store.SSHAudit `json:"items"`
	NextCursor *string          `json:"next_cursor" nullable:"true"`
}

type sshAuditOutput struct{ Body SshAuditPage }

type serverAuditInput struct {
	ID       string `path:"id" maxLength:"64"`
	Kind     string `query:"kind" maxLength:"32" doc:"connect, disconnect, exec, shell, subsystem, denied, auth_fail, host_key_mismatch or test"`
	Employee string `query:"employee" maxLength:"100" doc:"Employee name (dev-1); user for the user's test connections"`
	Cursor   string `query:"cursor" maxLength:"100"`
	Limit    int    `query:"limit" minimum:"0" maximum:"200"`
}

type projectAuditInput struct {
	PID      string `path:"pid" maxLength:"64"`
	ServerID string `query:"server_id" maxLength:"64"`
	ThreadID string `query:"thread_id" maxLength:"64"`
	Kind     string `query:"kind" maxLength:"32" doc:"connect, disconnect, exec, shell, subsystem, denied, auth_fail, host_key_mismatch or test"`
	Employee string `query:"employee" maxLength:"100" doc:"Employee name (dev-1); user for the user's test connections"`
	Cursor   string `query:"cursor" maxLength:"100"`
	Limit    int    `query:"limit" minimum:"0" maximum:"200"`
}

func (s *Server) auditPage(ctx context.Context, f store.SSHAuditFilter) (*sshAuditOutput, error) {
	rows, next, err := s.store.SSHAuditPage(ctx, f)
	if errors.Is(err, store.ErrBadCursor) {
		return nil, fail(http.StatusBadRequest, "bad_cursor", "cursor 不正确")
	}
	if err != nil {
		return nil, err
	}
	page := SshAuditPage{Items: rows}
	if next != "" {
		page.NextCursor = &next
	}
	return &sshAuditOutput{Body: page}, nil
}

func (s *Server) serverAudit(ctx context.Context, in *serverAuditInput) (*sshAuditOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.RemoteServer(ctx, p.User.ID, in.ID); err != nil {
		return nil, serverStoreErr(err)
	}
	return s.auditPage(ctx, store.SSHAuditFilter{UserID: p.User.ID, ServerID: in.ID, Kind: in.Kind, Employee: in.Employee,
		Cursor: in.Cursor, Limit: in.Limit})
}

func (s *Server) projectServerAudit(ctx context.Context, in *projectAuditInput) (*sshAuditOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ProjectOf(ctx, p.User.ID, in.PID); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	return s.auditPage(ctx, store.SSHAuditFilter{UserID: p.User.ID, ProjectID: in.PID, ServerID: in.ServerID, ThreadID: in.ThreadID,
		Kind: in.Kind, Employee: in.Employee, Cursor: in.Cursor, Limit: in.Limit})
}
