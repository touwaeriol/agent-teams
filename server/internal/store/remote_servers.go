package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
)

// Remote servers (讨论串 #38, remote-servers.md, data-model.md §2.14): the user's servers
// (at_remote_servers), the servers a project uses (at_project_servers), the roles they are granted to
// (at_project_server_grants), the session certificates the console signed (at_ssh_certs) and the
// gateway's audit trail (at_ssh_audit). Nothing here returns a credential except
// RemoteServerSecret / SealedRemoteServers, and those only return it sealed.

var (
	// ErrCredentialRequired: host, port, username or auth kind changed without a new credential
	// (the old one is sealed for the old values and would no longer open).
	ErrCredentialRequired = errors.New("改了主机、端口、用户名或认证方式，必须重新填写凭证")
	// ErrRoleNotGrantable: the coordinator, the lead, read-only roles and unknown roles cannot be
	// granted a server.
	ErrRoleNotGrantable = errors.New("这个岗位不能使用远程服务器")
	// ErrNotGranted: the session may not use that server (not in the project, role not granted,
	// session closed, coordinator / lead / read-only).
	ErrNotGranted = errors.New("not granted")
)

// MaxAuditCommand is the most command bytes an audit row keeps (longer ones are cut and flagged).
const MaxAuditCommand = 8 << 10

// SSHAuditRetention is how long at_ssh_audit rows are kept (daily cleanup).
const SSHAuditRetention = 365 * 24 * time.Hour

// SSHCertRetention is how long after valid_before an at_ssh_certs row is kept.
const SSHCertRetention = 30 * 24 * time.Hour

// RemoteServer is a row of at_remote_servers without its sealed credential.
type RemoteServer struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	Name               string     `json:"name"`
	Host               string     `json:"host"`
	Port               int        `json:"port"`
	Username           string     `json:"username"`
	AuthKind           string     `json:"auth_kind"`
	HasPassphrase      bool       `json:"has_passphrase"`
	KeyFingerprint     string     `json:"key_fingerprint"`
	HostKey            string     `json:"host_key"`
	HostKeyFP          string     `json:"host_key_fp" gorm:"column:host_key_fp"`
	HostKeyConfirmedAt *time.Time `json:"host_key_confirmed_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CredUpdatedAt      time.Time  `json:"cred_updated_at"`
}

// TableName maps RemoteServer to at_remote_servers.
func (RemoteServer) TableName() string { return "at_remote_servers" }

const remoteServerCols = `id, user_id, name, host, port, username, auth_kind, has_passphrase, key_fingerprint,
	host_key, host_key_fp, host_key_confirmed_at, created_at, updated_at, cred_updated_at`

// ServerCredential is a sealed credential with what may be shown about it.
type ServerCredential struct {
	Sealed         credvault.Sealed
	HasPassphrase  bool
	KeyFingerprint string // private_key: SHA256:… of the public key
}

func credColumns(c ServerCredential) map[string]any {
	return map[string]any{
		"cred_ciphertext": c.Sealed.Ciphertext, "cred_nonce": c.Sealed.Nonce, "wrapped_dek": c.Sealed.WrappedDEK,
		"dek_nonce": c.Sealed.DEKNonce, "kek_id": c.Sealed.KEKID, "has_passphrase": c.HasPassphrase,
		"key_fingerprint": c.KeyFingerprint, "cred_updated_at": gorm.Expr("now()"),
	}
}

func (s *Store) servers(ctx context.Context) *gorm.DB {
	return s.g(ctx).Model(&RemoteServer{}).Select(remoteServerCols)
}

// CreateRemoteServer stores a server with its credential. srv.ID must be set (NewID("srv")) because
// the credential's AAD names it. A name the user already has is ErrConflict.
func (s *Store) CreateRemoteServer(ctx context.Context, srv RemoteServer, cred ServerCredential) (RemoteServer, error) {
	if srv.ID == "" || srv.UserID == "" {
		return RemoteServer{}, errors.New("remote server: id and user_id are required")
	}
	row := credColumns(cred)
	for k, v := range map[string]any{"id": srv.ID, "user_id": srv.UserID, "name": srv.Name, "host": srv.Host,
		"port": srv.Port, "username": srv.Username, "auth_kind": srv.AuthKind} {
		row[k] = v
	}
	if err := s.g(ctx).Table("at_remote_servers").Create(row).Error; err != nil {
		return RemoteServer{}, mapErr(err)
	}
	return s.RemoteServer(ctx, srv.UserID, srv.ID)
}

// RemoteServers lists the user's servers by name.
func (s *Store) RemoteServers(ctx context.Context, userID string) ([]RemoteServer, error) {
	out := []RemoteServer{}
	err := s.servers(ctx).Where("user_id = ?", userID).Order("name").Find(&out).Error
	return out, err
}

// RemoteServer loads one of the user's servers.
func (s *Store) RemoteServer(ctx context.Context, userID, id string) (RemoteServer, error) {
	var out RemoteServer
	err := s.servers(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&out).Error
	return out, mapErr(err)
}

// RemoteServerByName loads one of the user's servers by name.
func (s *Store) RemoteServerByName(ctx context.Context, userID, name string) (RemoteServer, error) {
	var out RemoteServer
	err := s.servers(ctx).Where("user_id = ? AND name = ?", userID, name).Take(&out).Error
	return out, mapErr(err)
}

// RemoteServerSecret is a server with its sealed credential and host key MAC (gateway, test
// connection, reseal). Never send it to a client.
type RemoteServerSecret struct {
	RemoteServer
	Sealed     credvault.Sealed
	HostKeyMAC []byte
}

type secretRow struct {
	RemoteServer
	CredCiphertext []byte
	CredNonce      []byte
	WrappedDEK     []byte `gorm:"column:wrapped_dek"`
	DEKNonce       []byte `gorm:"column:dek_nonce"`
	KEKID          string `gorm:"column:kek_id"`
	HostKeyMAC     []byte `gorm:"column:host_key_mac"`
}

func (r secretRow) secret() RemoteServerSecret {
	return RemoteServerSecret{RemoteServer: r.RemoteServer, HostKeyMAC: r.HostKeyMAC, Sealed: credvault.Sealed{
		Ciphertext: r.CredCiphertext, Nonce: r.CredNonce, WrappedDEK: r.WrappedDEK, DEKNonce: r.DEKNonce, KEKID: r.KEKID}}
}

func (s *Store) secrets(ctx context.Context) *gorm.DB {
	return s.g(ctx).Table("at_remote_servers").
		Select(remoteServerCols + `, cred_ciphertext, cred_nonce, wrapped_dek, dek_nonce, kek_id, host_key_mac`)
}

// RemoteServerSecret loads one of the user's servers with its sealed credential.
func (s *Store) RemoteServerSecret(ctx context.Context, userID, id string) (RemoteServerSecret, error) {
	var row secretRow
	err := s.secrets(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&row).Error
	return row.secret(), mapErr(err)
}

// SealedRemoteServers lists every server of every user with its sealed credential (atctl admin reseal).
func (s *Store) SealedRemoteServers(ctx context.Context) ([]RemoteServerSecret, error) {
	var rows []secretRow
	if err := s.secrets(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RemoteServerSecret, len(rows))
	for i, r := range rows {
		out[i] = r.secret()
	}
	return out, nil
}

// ResealRemoteServer replaces a server's wrapped DEK and host key MAC (reseal; the ciphertext and
// cred_updated_at stay).
func (s *Store) ResealRemoteServer(ctx context.Context, id string, sealed credvault.Sealed, hostKeyMAC []byte) error {
	return Changed(s.g(ctx).Table("at_remote_servers").Where("id = ?", id).Updates(map[string]any{
		"wrapped_dek": sealed.WrappedDEK, "dek_nonce": sealed.DEKNonce, "kek_id": sealed.KEKID, "host_key_mac": hostKeyMAC}))
}

// RemoteServerPatch is a partial update; nil fields stay.
type RemoteServerPatch struct {
	Name     *string
	Host     *string
	Port     *int
	Username *string
	AuthKind *string
}

// SealFunc seals a new credential for the server as it will be after the update (merged: the
// current row with the patch applied, so ServerAAD gets the final user, id, kind, host, port and
// username). It returns nil to keep the current credential.
type SealFunc func(merged RemoteServer) (*ServerCredential, error)

// UpdateRemoteServer changes a server and, when seal returns a credential, replaces it. The row is
// locked and seal runs inside the same transaction, so a concurrent update cannot leave a
// credential sealed for values the row no longer has. A change of host, port, username or auth kind
// without a new credential is ErrCredentialRequired; a change of host or port also drops the
// confirmed host key. Renaming revokes the certificates that name the old name (returned, for the
// gateway to cut their connections). seal may be nil.
func (s *Store) UpdateRemoteServer(ctx context.Context, userID, id string, p RemoteServerPatch, seal SealFunc) (RemoteServer, []int64, error) {
	var (
		out     RemoteServer
		revoked []int64
	)
	err := s.Tx(ctx, func(tx *Store) error {
		var cur RemoteServer
		if err := tx.servers(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", id, userID).Take(&cur).Error; err != nil {
			return mapErr(err)
		}
		merged := cur
		row := map[string]any{"updated_at": gorm.Expr("now()")}
		bound, hostMoved := false, false // bound: a value the credential's AAD names changed
		if p.Name != nil && *p.Name != cur.Name {
			merged.Name, row["name"] = *p.Name, *p.Name
		}
		if p.Host != nil && *p.Host != cur.Host {
			merged.Host, row["host"] = *p.Host, *p.Host
			bound, hostMoved = true, true
		}
		if p.Port != nil && *p.Port != cur.Port {
			merged.Port, row["port"] = *p.Port, *p.Port
			bound, hostMoved = true, true
		}
		if p.Username != nil && *p.Username != cur.Username {
			merged.Username, row["username"] = *p.Username, *p.Username
			bound = true
		}
		if p.AuthKind != nil && *p.AuthKind != cur.AuthKind {
			merged.AuthKind, row["auth_kind"] = *p.AuthKind, *p.AuthKind
			bound = true
		}
		var cred *ServerCredential
		if seal != nil {
			var err error
			if cred, err = seal(merged); err != nil {
				return err
			}
		}
		if bound && cred == nil {
			return ErrCredentialRequired
		}
		if cred != nil {
			for k, v := range credColumns(*cred) {
				row[k] = v
			}
		}
		if hostMoved {
			row["host_key"], row["host_key_fp"], row["host_key_mac"], row["host_key_confirmed_at"] = "", "", nil, nil
		}
		if err := tx.g(ctx).Table("at_remote_servers").Where("id = ?", id).Updates(row).Error; err != nil {
			return mapErr(err)
		}
		if merged.Name != cur.Name {
			var err error
			if revoked, err = tx.RevokeServerCerts(ctx, userID, "", cur.Name, "server_renamed"); err != nil {
				return err
			}
		}
		var err error
		out, err = tx.RemoteServer(ctx, userID, id)
		return err
	})
	return out, revoked, err
}

// PinRemoteServerHostKey stores the host key a test connection saw, only while the server still
// has no host key and still points at host:port (a concurrent edit or another pin wins). pinned
// reports whether this call stored it.
func (s *Store) PinRemoteServerHostKey(ctx context.Context, userID, id, host string, port int, key, fp string, mac []byte) (bool, error) {
	res := s.g(ctx).Table("at_remote_servers").
		Where("id = ? AND user_id = ? AND host = ? AND port = ? AND host_key = ''", id, userID, host, port).
		Updates(map[string]any{"host_key": key, "host_key_fp": fp, "host_key_mac": mac, "host_key_confirmed_at": gorm.Expr("now()"),
			"updated_at": gorm.Expr("now()")})
	return res.RowsAffected == 1, mapErr(res.Error)
}

// SetRemoteServerHostKey records the host key the user confirmed (key = authorized_keys line,
// mac = credvault HostKeyMACData under the vault); key "" forgets it.
func (s *Store) SetRemoteServerHostKey(ctx context.Context, userID, id, key, fp string, mac []byte) (RemoteServer, error) {
	row := map[string]any{"host_key": key, "host_key_fp": fp, "host_key_mac": mac, "host_key_confirmed_at": gorm.Expr("now()"),
		"updated_at": gorm.Expr("now()")}
	if key == "" {
		row["host_key_fp"], row["host_key_mac"], row["host_key_confirmed_at"] = "", nil, nil
	}
	if err := Changed(s.g(ctx).Table("at_remote_servers").Where("id = ? AND user_id = ?", id, userID).Updates(row)); err != nil {
		return RemoteServer{}, err
	}
	return s.RemoteServer(ctx, userID, id)
}

// DeleteRemoteServer removes a server (and with it its project links and grants) and revokes the
// certificates that name it; the revoked serials are returned. Audit rows stay.
func (s *Store) DeleteRemoteServer(ctx context.Context, userID, id string) ([]int64, error) {
	var revoked []int64
	err := s.Tx(ctx, func(tx *Store) error {
		cur, err := tx.RemoteServer(ctx, userID, id)
		if err != nil {
			return err
		}
		if revoked, err = tx.RevokeServerCerts(ctx, userID, "", cur.Name, "server_removed"); err != nil {
			return err
		}
		return Changed(tx.g(ctx).Exec(`DELETE FROM at_remote_servers WHERE id = ? AND user_id = ?`, id, userID))
	})
	return revoked, err
}

// ---- projects and grants ------------------------------------------------------------------

// ProjectServer is a server a project uses, with the roles it is granted to.
type ProjectServer struct {
	Server RemoteServer `json:"server"`
	Roles  []string     `json:"roles"`
}

// ProjectServers lists the servers of one of the user's projects by name.
func (s *Store) ProjectServers(ctx context.Context, userID, projectID string) ([]ProjectServer, error) {
	servers := []RemoteServer{}
	err := s.g(ctx).Model(&RemoteServer{}).Select(prefixCols("r", remoteServerCols)).Table("at_remote_servers r").
		Joins("JOIN at_project_servers ps ON ps.server_id = r.id").
		Where("ps.project_id = ? AND ps.user_id = ?", projectID, userID).Order("r.name").Find(&servers).Error
	if err != nil {
		return nil, err
	}
	var grants []struct{ ServerID, Role string }
	err = s.g(ctx).Table("at_project_server_grants").Select("server_id, role").
		Where("project_id = ?", projectID).Order("role").Find(&grants).Error
	if err != nil {
		return nil, err
	}
	roles := map[string][]string{}
	for _, g := range grants {
		roles[g.ServerID] = append(roles[g.ServerID], g.Role)
	}
	out := make([]ProjectServer, len(servers))
	for i, srv := range servers {
		out[i] = ProjectServer{Server: srv, Roles: roles[srv.ID]}
		if out[i].Roles == nil {
			out[i].Roles = []string{}
		}
	}
	return out, nil
}

func prefixCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// ServerProjectIDs lists the user's projects that use a server.
func (s *Store) ServerProjectIDs(ctx context.Context, userID, serverID string) ([]string, error) {
	var out []string
	err := s.g(ctx).Table("at_project_servers").Where("server_id = ? AND user_id = ?", serverID, userID).
		Order("project_id").Pluck("project_id", &out).Error
	if out == nil {
		out = []string{}
	}
	return out, err
}

// ServerProjects maps each of the user's servers to the projects using it (by project name).
func (s *Store) ServerProjects(ctx context.Context, userID string) (map[string][]ProjectRef, error) {
	var rows []struct{ ServerID, ID, Name string }
	err := s.g(ctx).Table("at_project_servers ps").Select("ps.server_id, p.id, p.name").
		Joins("JOIN at_projects p ON p.id = ps.project_id").Where("ps.user_id = ?", userID).
		Order("p.name").Find(&rows).Error
	out := map[string][]ProjectRef{}
	for _, r := range rows {
		out[r.ServerID] = append(out[r.ServerID], ProjectRef{ID: r.ID, Name: r.Name})
	}
	return out, err
}

// ProjectServerSetting is one server of a project with the roles it is granted to.
type ProjectServerSetting struct {
	ServerID string
	Roles    []string
}

// SetProjectServers makes list the project's servers and grants, in one transaction: servers not
// in list leave the project, new ones join, and every listed server gets exactly its roles
// (GrantableRoles). The certificates revoked on the way are returned. A server or project that is
// not the user's is ErrNotFound; a role that cannot be granted is ErrRoleNotGrantable.
func (s *Store) SetProjectServers(ctx context.Context, userID, projectID string, list []ProjectServerSetting) ([]int64, error) {
	var revoked []int64
	err := s.Tx(ctx, func(tx *Store) error {
		var n int64
		if err := tx.g(ctx).Table("at_projects").Where("id = ? AND user_id = ?", projectID, userID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		current, err := tx.ProjectServers(ctx, userID, projectID)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, ps := range list {
			want[ps.ServerID] = true
		}
		for _, ps := range current {
			if !want[ps.Server.ID] {
				r, err := tx.RemoveProjectServer(ctx, userID, projectID, ps.Server.ID)
				if err != nil {
					return err
				}
				revoked = append(revoked, r...)
			}
		}
		for _, ps := range list {
			if err := tx.AddProjectServer(ctx, userID, projectID, ps.ServerID); err != nil {
				return err
			}
			r, err := tx.SetProjectServerGrants(ctx, userID, projectID, ps.ServerID, ps.Roles)
			if err != nil {
				return err
			}
			revoked = append(revoked, r...)
		}
		return nil
	})
	return revoked, err
}

// AddProjectServer lets a project use one of the user's servers (no role yet). Both must be the
// user's (ErrNotFound otherwise); adding it again is a no-op.
func (s *Store) AddProjectServer(ctx context.Context, userID, projectID, serverID string) error {
	res, err := Exec(ctx, s.db, `INSERT INTO at_project_servers (project_id, server_id, user_id)
		SELECT p.id, r.id, p.user_id FROM at_projects p JOIN at_remote_servers r ON r.user_id = p.user_id
		 WHERE p.id = $1 AND r.id = $2 AND p.user_id = $3
		ON CONFLICT DO NOTHING`, projectID, serverID, userID)
	if err != nil {
		return mapErr(err)
	}
	if res.RowsAffected() == 0 {
		var n int64
		if err := s.g(ctx).Table("at_project_servers").Where("project_id = ? AND server_id = ? AND user_id = ?",
			projectID, serverID, userID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// RemoveProjectServer stops a project using a server (its grants go too) and revokes the project's
// certificates that name it.
func (s *Store) RemoveProjectServer(ctx context.Context, userID, projectID, serverID string) ([]int64, error) {
	var revoked []int64
	err := s.Tx(ctx, func(tx *Store) error {
		srv, err := tx.RemoteServer(ctx, userID, serverID)
		if err != nil {
			return err
		}
		if err := Changed(tx.g(ctx).Exec(`DELETE FROM at_project_servers WHERE project_id = ? AND server_id = ? AND user_id = ?`,
			projectID, serverID, userID)); err != nil {
			return err
		}
		revoked, err = tx.RevokeServerCerts(ctx, userID, projectID, srv.Name, "server_removed")
		return err
	})
	return revoked, err
}

// GrantableRoles checks roles may be granted a server in the project: each must be a member role
// of the user (the project's copy, else the template) that is not read-only. The first that is not
// is reported in an ErrRoleNotGrantable error.
func (s *Store) GrantableRoles(ctx context.Context, userID, projectID string, roles []string) error {
	if len(roles) == 0 {
		return nil
	}
	type info struct {
		Role     string
		Tier     string
		ReadOnly bool
		Gate     string
	}
	var rows []info
	err := Raw(ctx, s.db, `SELECT DISTINCT ON (role) role, tier, read_only, gate FROM at_roles
		 WHERE user_id = $1 AND (project_id = $2 OR project_id IS NULL) AND role = ANY($3)
		 ORDER BY role, project_id NULLS LAST`, userID, projectID, TextArray(roles)).Scan(&rows).Error
	if err != nil {
		return err
	}
	byRole := map[string]info{}
	for _, r := range rows {
		byRole[r.Role] = r
	}
	for _, role := range roles {
		r, ok := byRole[role]
		switch {
		case !ok:
			return fmt.Errorf("%w：没有岗位 %q", ErrRoleNotGrantable, role)
		case r.Tier != "member":
			return fmt.Errorf("%w：%q 是统筹或组长", ErrRoleNotGrantable, role)
		case r.ReadOnly || r.Gate == "review" || r.Gate == "decision":
			return fmt.Errorf("%w：%q 是只读岗位", ErrRoleNotGrantable, role)
		}
	}
	return nil
}

// SetProjectServerGrants replaces the roles of a project that may use a server (the server must be
// in the project: ErrNotFound otherwise). Roles are checked with GrantableRoles. Certificates of the
// project naming the server whose role lost the grant are revoked and returned.
func (s *Store) SetProjectServerGrants(ctx context.Context, userID, projectID, serverID string, roles []string) ([]int64, error) {
	roles = uniqueSorted(roles)
	var revoked []int64
	err := s.Tx(ctx, func(tx *Store) error {
		var srv RemoteServer
		err := tx.g(ctx).Model(&RemoteServer{}).Select(prefixCols("r", remoteServerCols)).Table("at_remote_servers r").
			Joins("JOIN at_project_servers ps ON ps.server_id = r.id").
			Where("ps.project_id = ? AND ps.server_id = ? AND ps.user_id = ?", projectID, serverID, userID).
			Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "ps"}}).Take(&srv).Error
		if err != nil {
			return mapErr(err)
		}
		if err := tx.GrantableRoles(ctx, userID, projectID, roles); err != nil {
			return err
		}
		if _, err := Exec(ctx, tx.db, `DELETE FROM at_project_server_grants
			 WHERE project_id = $1 AND server_id = $2 AND NOT (role = ANY($3))`, projectID, serverID, TextArray(roles)); err != nil {
			return err
		}
		if _, err := Exec(ctx, tx.db, `INSERT INTO at_project_server_grants (project_id, server_id, role)
			SELECT $1, $2, unnest($3::text[]) ON CONFLICT DO NOTHING`, projectID, serverID, TextArray(roles)); err != nil {
			return mapErr(err)
		}
		revoked, err = int64s(Query(ctx, tx.db, `UPDATE at_ssh_certs SET revoked_at = now(), revoke_reason = 'grant_removed'
			 WHERE revoked_at IS NULL AND project_id = $1 AND $2 = ANY(principals) AND NOT (role = ANY($3))
			 RETURNING serial`, projectID, srv.Name, TextArray(roles)))
		return err
	})
	return revoked, err
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- sessions ------------------------------------------------------------------------------

// SSHSession is what the gateway and certificate signing need to know about a session.
type SSHSession struct {
	SessionID  string
	State      string
	OpenGen    int
	NodeID     string
	EmployeeID string
	Employee   string // name (dev-1)
	Tier       string
	Role       string
	ReadOnly   bool
	ProjectID  string
	ThreadID   string
	UserID     string // the project's owner
}

// MayUseSSH reports whether the session may have servers at all: a live member session of a role
// that is not read-only. The coordinator and the lead never may.
func (x SSHSession) MayUseSSH() bool {
	return x.State != "closed" && x.Tier == "member" && !x.ReadOnly
}

// sshSessionSelect reads an employee's read-only flag from its spec (store.Role JSON): read-only
// when read_only is true or the gate is review/decision, whatever read_only says (the same rule as
// GrantableRoles: a review or decision role never gets a server, #38).
const sshSessionSelect = `SELECT a.id, a.state, a.open_gen, COALESCE(a.node_id, ''), e.id, e.name, e.tier, e.role,
	(CASE WHEN jsonb_typeof(e.spec->'read_only') = 'boolean' THEN (e.spec->>'read_only')::boolean ELSE false END
	 OR COALESCE(e.spec->>'gate', '') IN ('review', 'decision')),
	e.project_id, COALESCE(a.thread_id, e.thread_id, ''), p.user_id
	FROM at_agent_sessions a JOIN at_employees e ON e.id = a.employee_id JOIN at_projects p ON p.id = e.project_id`

func scanSSHSession(r Scanner) (SSHSession, error) {
	var x SSHSession
	err := r.Scan(&x.SessionID, &x.State, &x.OpenGen, &x.NodeID, &x.EmployeeID, &x.Employee, &x.Tier, &x.Role, &x.ReadOnly,
		&x.ProjectID, &x.ThreadID, &x.UserID)
	return x, err
}

// SSHSession loads a session (any state) for SSH checks.
func (s *Store) SSHSession(ctx context.Context, sessionID string) (SSHSession, error) {
	rows, err := Query(ctx, s.db, sshSessionSelect+` WHERE a.id = $1`, sessionID)
	list, err := Collect(rows, err, scanSSHSession)
	if err != nil {
		return SSHSession{}, err
	}
	if len(list) == 0 {
		return SSHSession{}, ErrNotFound
	}
	return list[0], nil
}

// LiveSSHSessions lists the live sessions of a project that may use servers (MayUseSSH), for
// ssh.servers_update after a project's servers or grants change.
func (s *Store) LiveSSHSessions(ctx context.Context, projectID string) ([]SSHSession, error) {
	rows, err := Query(ctx, s.db, sshSessionSelect+` WHERE e.project_id = $1 AND a.state <> 'closed'
		ORDER BY a.id`, projectID)
	list, err := Collect(rows, err, scanSSHSession)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, x := range list {
		if x.MayUseSSH() {
			out = append(out, x)
		}
	}
	return out, nil
}

// SessionServers returns the session and the servers it may use now (granted to its role in its
// project), by name. A session that may not use SSH at all gets none.
func (s *Store) SessionServers(ctx context.Context, sessionID string) (SSHSession, []RemoteServer, error) {
	x, err := s.SSHSession(ctx, sessionID)
	if err != nil {
		return SSHSession{}, nil, err
	}
	out := []RemoteServer{}
	if !x.MayUseSSH() {
		return x, out, nil
	}
	err = s.g(ctx).Model(&RemoteServer{}).Select(prefixCols("r", remoteServerCols)).Table("at_remote_servers r").
		Joins("JOIN at_project_servers ps ON ps.server_id = r.id").
		Joins("JOIN at_project_server_grants g ON g.project_id = ps.project_id AND g.server_id = ps.server_id").
		Where("ps.project_id = ? AND ps.user_id = ? AND g.role = ?", x.ProjectID, x.UserID, x.Role).
		Order("r.name").Find(&out).Error
	return x, out, err
}

// SessionServerGrant is the gateway's per-use check: the session, and the server named name with
// its sealed credential, when the session may use it now. ErrNotGranted otherwise (also for a
// closed session or one that may not use SSH); ErrNotFound for an unknown session.
func (s *Store) SessionServerGrant(ctx context.Context, sessionID, name string) (SSHSession, RemoteServerSecret, error) {
	x, err := s.SSHSession(ctx, sessionID)
	if err != nil {
		return SSHSession{}, RemoteServerSecret{}, err
	}
	if !x.MayUseSSH() {
		return x, RemoteServerSecret{}, ErrNotGranted
	}
	var row secretRow
	err = s.g(ctx).Table("at_remote_servers r").
		Select(prefixCols("r", remoteServerCols+`, cred_ciphertext, cred_nonce, wrapped_dek, dek_nonce, kek_id, host_key_mac`)).
		Joins("JOIN at_project_servers ps ON ps.server_id = r.id").
		Joins("JOIN at_project_server_grants g ON g.project_id = ps.project_id AND g.server_id = ps.server_id").
		Where("ps.project_id = ? AND ps.user_id = ? AND g.role = ? AND r.name = ?", x.ProjectID, x.UserID, x.Role, name).
		Take(&row).Error
	if IsNoRows(err) {
		return x, RemoteServerSecret{}, ErrNotGranted
	}
	if err != nil {
		return x, RemoteServerSecret{}, err
	}
	return x, row.secret(), nil
}

// ---- certificates --------------------------------------------------------------------------

// SSHCert is a row of at_ssh_certs.
type SSHCert struct {
	Serial       int64
	SessionID    string
	OpenGen      int
	NodeID       string
	ProjectID    string
	ThreadID     string
	EmployeeID   string
	Role         string
	PubkeyFP     string
	Principals   []string
	CAID         string
	ValidAfter   time.Time
	ValidBefore  time.Time
	RevokedAt    *time.Time
	RevokeReason string
	CreatedAt    time.Time
}

const sshCertCols = `serial, session_id, open_gen, node_id, project_id, COALESCE(thread_id, ''), employee_id, role, pubkey_fp,
	principals, ca_id, valid_after, valid_before, revoked_at, revoke_reason, created_at`

func scanSSHCert(r Scanner) (SSHCert, error) {
	var (
		c          SSHCert
		principals TextArray
	)
	err := r.Scan(&c.Serial, &c.SessionID, &c.OpenGen, &c.NodeID, &c.ProjectID, &c.ThreadID, &c.EmployeeID, &c.Role,
		&c.PubkeyFP, &principals, &c.CAID, &c.ValidAfter, &c.ValidBefore, &c.RevokedAt, &c.RevokeReason, &c.CreatedAt)
	c.Principals = []string(principals)
	return c, err
}

// InsertSSHCert records a certificate about to be signed and returns it with its serial (the
// certificate's serial number).
func (s *Store) InsertSSHCert(ctx context.Context, c SSHCert) (SSHCert, error) {
	rows, err := Query(ctx, s.db, `INSERT INTO at_ssh_certs (session_id, open_gen, node_id, project_id, thread_id,
		employee_id, role, pubkey_fp, principals, ca_id, valid_after, valid_before)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11, $12) RETURNING `+sshCertCols,
		c.SessionID, c.OpenGen, c.NodeID, c.ProjectID, c.ThreadID, c.EmployeeID, c.Role, c.PubkeyFP, TextArray(c.Principals),
		c.CAID, c.ValidAfter, c.ValidBefore)
	list, err := Collect(rows, err, scanSSHCert)
	if err != nil {
		return SSHCert{}, mapErr(err)
	}
	return list[0], nil
}

// SSHCert loads a certificate by serial.
func (s *Store) SSHCert(ctx context.Context, serial int64) (SSHCert, error) {
	rows, err := Query(ctx, s.db, `SELECT `+sshCertCols+` FROM at_ssh_certs WHERE serial = $1`, serial)
	list, err := Collect(rows, err, scanSSHCert)
	if err != nil {
		return SSHCert{}, err
	}
	if len(list) == 0 {
		return SSHCert{}, ErrNotFound
	}
	return list[0], nil
}

func int64s(rows *Rows, err error) ([]int64, error) {
	return Collect(rows, err, func(r Scanner) (int64, error) {
		var n int64
		return n, r.Scan(&n)
	})
}

// RevokeSessionCerts revokes every live certificate of a session (closed, crashed, thread done…).
func (s *Store) RevokeSessionCerts(ctx context.Context, sessionID, reason string) ([]int64, error) {
	return int64s(Query(ctx, s.db, `UPDATE at_ssh_certs SET revoked_at = now(), revoke_reason = $2
		 WHERE session_id = $1 AND revoked_at IS NULL RETURNING serial`, sessionID, reason))
}

// RevokeOldGenCerts revokes a session's certificates of generations before gen (the session was
// opened again).
func (s *Store) RevokeOldGenCerts(ctx context.Context, sessionID string, gen int, reason string) ([]int64, error) {
	return int64s(Query(ctx, s.db, `UPDATE at_ssh_certs SET revoked_at = now(), revoke_reason = $3
		 WHERE session_id = $1 AND open_gen < $2 AND revoked_at IS NULL RETURNING serial`, sessionID, gen, reason))
}

// RevokeServerCerts revokes live certificates naming the server name in the user's projects (or
// only in projectID when it is not "").
func (s *Store) RevokeServerCerts(ctx context.Context, userID, projectID, name, reason string) ([]int64, error) {
	return int64s(Query(ctx, s.db, `UPDATE at_ssh_certs SET revoked_at = now(), revoke_reason = $4
		 WHERE revoked_at IS NULL AND $3 = ANY(principals)
		   AND project_id IN (SELECT id FROM at_projects WHERE user_id = $1 AND ($2 = '' OR id = $2))
		 RETURNING serial`, userID, projectID, name, reason))
}

// DeleteExpiredSSHCerts removes certificates that expired before cutoff (daily cleanup).
func (s *Store) DeleteExpiredSSHCerts(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := Exec(ctx, s.db, `DELETE FROM at_ssh_certs WHERE valid_before < $1`, cutoff)
	return res.RowsAffected(), err
}

// ---- audit ---------------------------------------------------------------------------------

// SSH audit kinds (at_ssh_audit.kind).
const (
	SSHAuditConnect         = "connect"
	SSHAuditDisconnect      = "disconnect"
	SSHAuditExec            = "exec"
	SSHAuditShell           = "shell"
	SSHAuditSubsystem       = "subsystem"
	SSHAuditDenied          = "denied"
	SSHAuditAuthFail        = "auth_fail"
	SSHAuditHostKeyMismatch = "host_key_mismatch"
	SSHAuditTest            = "test" // the user's "test connection"
)

// SSHAudit is a row of at_ssh_audit. JSON tags are the SshAuditView of api.md.
type SSHAudit struct {
	ID               int64          `json:"id"`
	CreatedAt        time.Time      `json:"created_at"`
	UserID           string         `json:"-"`
	ProjectID        string         `json:"project_id"`
	ThreadID         string         `json:"thread_id"`
	SessionID        string         `json:"session_id"`
	EmployeeID       string         `json:"employee_id"`
	Employee         string         `json:"employee"`
	Role             string         `json:"role"`
	NodeID           string         `json:"node_id"`
	ServerID         string         `json:"server_id"`
	ServerName       string         `json:"server_name"`
	CertSerial       *int64         `json:"cert_serial"`
	ConnID           string         `json:"conn_id"`
	Kind             string         `json:"kind"`
	Command          string         `json:"command"`
	CommandTruncated bool           `json:"command_truncated"`
	ExitCode         *int           `json:"exit_code"`
	DurationMS       *int64         `json:"duration_ms" gorm:"column:duration_ms"`
	BytesIn          int64          `json:"bytes_in"`
	BytesOut         int64          `json:"bytes_out"`
	Reason           string         `json:"reason"`
	Detail           map[string]any `json:"detail" gorm:"serializer:jsonb"`
}

// TableName maps SSHAudit to at_ssh_audit.
func (SSHAudit) TableName() string { return "at_ssh_audit" }

// truncateUTF8 cuts s to at most n bytes on a rune boundary.
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// InsertSSHAudit writes an audit row (the command must already be redacted; it is cut to
// MaxAuditCommand bytes here) and returns it with its id.
func (s *Store) InsertSSHAudit(ctx context.Context, a SSHAudit) (SSHAudit, error) {
	var cut bool
	a.Command, cut = truncateUTF8(a.Command, MaxAuditCommand)
	a.CommandTruncated = a.CommandTruncated || cut
	if a.Detail == nil {
		a.Detail = map[string]any{}
	}
	a.ID = 0
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	err := s.g(ctx).Omit("id").Create(&a).Error
	return a, mapErr(err)
}

// SSHAuditFilter selects audit rows of one user; empty fields do not filter.
type SSHAuditFilter struct {
	UserID    string
	ProjectID string
	ServerID  string
	ThreadID  string
	SessionID string
	Employee  string
	Kind      string
	Cursor    string // next of the previous page
	Limit     int    // default 50, at most 200
}

// SSHAuditPage returns audit rows newest first and the cursor of the next page ("" = last page).
func (s *Store) SSHAuditPage(ctx context.Context, f SSHAuditFilter) ([]SSHAudit, string, error) {
	if f.UserID == "" {
		return nil, "", errors.New("ssh audit: user_id is required")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := s.g(ctx).Model(&SSHAudit{}).Where("user_id = ?", f.UserID)
	for col, v := range map[string]string{"project_id": f.ProjectID, "server_id": f.ServerID, "thread_id": f.ThreadID,
		"session_id": f.SessionID, "employee": f.Employee, "kind": f.Kind} {
		if v != "" {
			q = q.Where(col+" = ?", v)
		}
	}
	if f.Cursor != "" {
		at, id, err := parseAuditCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		q = q.Where("(created_at, id) < (?, ?)", at, id)
	}
	out := []SSHAudit{}
	if err := q.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&out).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = strconv.FormatInt(last.CreatedAt.UnixMicro(), 10) + "_" + strconv.FormatInt(last.ID, 10)
	}
	return out, next, nil
}

// ErrBadCursor is a cursor SSHAuditPage did not hand out.
var ErrBadCursor = errors.New("invalid cursor")

func parseAuditCursor(c string) (time.Time, int64, error) {
	ts, id, ok := strings.Cut(c, "_")
	us, err1 := strconv.ParseInt(ts, 10, 64)
	n, err2 := strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return time.Time{}, 0, ErrBadCursor
	}
	return time.UnixMicro(us), n, nil
}

// DeleteOldSSHAudit removes audit rows older than cutoff (daily cleanup, SSHAuditRetention).
func (s *Store) DeleteOldSSHAudit(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := Exec(ctx, s.db, `DELETE FROM at_ssh_audit WHERE created_at < $1`, cutoff)
	return res.RowsAffected(), err
}

// PruneSSH is the daily cleanup of the remote server tables: audit rows past SSHAuditRetention and
// certificates expired more than SSHCertRetention ago.
func (s *Store) PruneSSH(ctx context.Context, now time.Time) (audit, certs int64, err error) {
	if audit, err = s.DeleteOldSSHAudit(ctx, now.Add(-SSHAuditRetention)); err != nil {
		return 0, 0, err
	}
	certs, err = s.DeleteExpiredSSHCerts(ctx, now.Add(-SSHCertRetention))
	return audit, certs, err
}
