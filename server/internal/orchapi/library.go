package orchapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
)

// UploadBody is POST /api/projects/{pid}/library: one file the user adds to the library.
type UploadBody struct {
	Name  string `json:"name" minLength:"1" maxLength:"300" doc:"File name; its extension decides the type (same list as publish_file)"`
	Data  string `json:"data" minLength:"1" maxLength:"6990508" doc:"Base64 of the file, at most 5 MB"`
	Title string `json:"title,omitempty" maxLength:"300" doc:"Shown name; default the file name"`
}

type uploadInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body UploadBody
}

type usageQuery struct {
	PID  string `path:"pid" maxLength:"64"`
	Days int    `query:"days" minimum:"0" maximum:"90" doc:"Window in days (default 7)"`
}

// ProjectUsage is GET /api/projects/{pid}/usage: what the project's Claude sessions used.
type ProjectUsage struct {
	Days       int                    `json:"days"`
	CostUSDEst float64                `json:"cost_usd_est"`
	Tokens     UsageTokens            `json:"tokens"`
	Turns      int64                  `json:"turns"`
	ByDay      []DayUsage             `json:"by_day"`
	ByThread   []ThreadUsage          `json:"by_thread"`
	ByEmployee []ProjectEmployeeUsage `json:"by_employee"`
}

// DayUsage is one day of a project's usage (UTC days).
type DayUsage struct {
	Day     string      `json:"day"`
	CostUSD float64     `json:"cost_usd"`
	Tokens  UsageTokens `json:"tokens"`
}

// ThreadUsage is one thread's share; thread_no null = the project coordinator.
type ThreadUsage struct {
	ThreadNo *int        `json:"thread_no"`
	Title    string      `json:"title"`
	CostUSD  float64     `json:"cost_usd"`
	Tokens   UsageTokens `json:"tokens"`
}

// ProjectEmployeeUsage is one employee's share within its thread.
type ProjectEmployeeUsage struct {
	Employee string      `json:"employee"`
	ThreadNo *int        `json:"thread_no"`
	CostUSD  float64     `json:"cost_usd"`
	Tokens   UsageTokens `json:"tokens"`
}

type projectUsageOutput struct{ Body ProjectUsage }

func (a *API) registerLibrary(api huma.API) {
	t := "deliverables"
	huma.Register(api, op("upload-library-file", "POST", "/api/projects/{pid}/library", t, status(201),
		summary("Add a file to the project library (stored and served like publish_file)"),
		func(o *huma.Operation) { o.MaxBodyBytes = 8 << 20 }), a.uploadLibraryFile)
	huma.Register(api, op("project-usage", "GET", "/api/projects/{pid}/usage", "projects",
		summary("Tokens and estimated cost of the project's sessions, per day, thread and employee")), a.projectUsage)
}

// LibraryKind is the deliverable kind of an uploaded file, from how it renders.
func LibraryKind(contentType string) string {
	switch artifacts.Render(contentType) {
	case "html", "image":
		return "design"
	case "markdown", "pdf":
		return "doc"
	}
	if strings.HasPrefix(contentType, "application/json") || strings.HasPrefix(contentType, "text/csv") {
		return "data"
	}
	return "doc"
}

// libraryFile checks an upload and turns it into the stored file.
func libraryFile(b UploadBody) (artifacts.ArtifactFile, error) {
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(b.Name, "\\", "/")))
	if !artifacts.ValidPath(name) {
		return artifacts.ArtifactFile{}, httpapi.Invalid("name",
			"不支持的文件："+name+"（支持 html、md、svg、png、jpg、gif、webp、pdf、json、csv、txt）")
	}
	data, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil || len(data) == 0 {
		return artifacts.ArtifactFile{}, httpapi.Invalid("data", "文件内容不对")
	}
	if len(data) > artifacts.MaxBytes {
		return artifacts.ArtifactFile{}, httpapi.Invalid("data", fmt.Sprintf("文件超过 %d MB", artifacts.MaxBytes>>20))
	}
	sum := sha256.Sum256(data)
	return artifacts.ArtifactFile{Path: name, ContentType: artifacts.ContentType(name), Size: len(data),
		SHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}

func (a *API) uploadLibraryFile(ctx context.Context, in *uploadInput) (*deliverableOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	f, err := libraryFile(in.Body)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Body.Title)
	if title == "" {
		title = f.Path
	}
	files := []artifacts.ArtifactFile{f}
	var d core.Deliverable
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		d, err = core.InsertDeliverable(ctx, tx, core.NewDeliverable{ProjectID: p.ID, Kind: LibraryKind(f.ContentType), Title: title,
			Detail: artifacts.Detail("user", 1, f.Path, files, nil), PublishedBy: "user"})
		if err != nil {
			return err
		}
		if err := artifacts.Save(ctx, tx, d.ID, 1, "user", files); err != nil {
			return err
		}
		return tx.EmitDeliverable(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	v, err := core.DeliverableViewOf(ctx, a.e.Core.Pool, d)
	return &deliverableOutput{Body: v}, err
}

// storageBytes is what the project's hosted files take, every version counted.
func (a *API) storageBytes(ctx context.Context, projectID string) (int64, error) {
	var n int64
	err := a.e.Core.Pool.QueryRow(ctx, `SELECT COALESCE(sum(f.size), 0)::bigint FROM at_deliverable_files f
		JOIN at_deliverables d ON d.id = f.deliverable_id WHERE d.project_id = $1`, projectID).Scan(&n)
	return n, err
}

func usageDays(n int) int {
	if n <= 0 {
		return 7
	}
	return n
}

const usageSums = `COALESCE(sum(u.cost_usd), 0)::float8, COALESCE(sum(u.input_tokens),0)::bigint, COALESCE(sum(u.output_tokens),0)::bigint,
	COALESCE(sum(u.cache_read_tokens),0)::bigint, COALESCE(sum(u.cache_write_tokens),0)::bigint`

func (a *API) projectUsage(ctx context.Context, in *usageQuery) (*projectUsageOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	out := ProjectUsage{Days: usageDays(in.Days), ByDay: []DayUsage{}, ByThread: []ThreadUsage{}, ByEmployee: []ProjectEmployeeUsage{}}
	since := time.Now().Add(-time.Duration(out.Days) * 24 * time.Hour)
	steps := []func(context.Context, string, time.Time, *ProjectUsage) error{a.usageTotals, a.usageByDay, a.usageByThread, a.usageByEmployee}
	for _, step := range steps {
		if err := step(ctx, p.ID, since, &out); err != nil {
			return nil, err
		}
	}
	return &projectUsageOutput{Body: out}, nil
}

func (a *API) usageTotals(ctx context.Context, pid string, since time.Time, out *ProjectUsage) error {
	t := &out.Tokens
	return a.e.Core.Pool.QueryRow(ctx, `SELECT count(*), `+usageSums+` FROM at_usage u WHERE u.project_id = $1 AND u.created_at >= $2`,
		pid, since).Scan(&out.Turns, &out.CostUSDEst, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite)
}

func (a *API) usageByDay(ctx context.Context, pid string, since time.Time, out *ProjectUsage) error {
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT to_char(date_trunc('day', u.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), `+usageSums+`
		FROM at_usage u WHERE u.project_id = $1 AND u.created_at >= $2 GROUP BY 1 ORDER BY 1`, pid, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d DayUsage
		if err := rows.Scan(&d.Day, &d.CostUSD, &d.Tokens.Input, &d.Tokens.Output, &d.Tokens.CacheRead, &d.Tokens.CacheWrite); err != nil {
			return err
		}
		out.ByDay = append(out.ByDay, d)
	}
	return rows.Err()
}

func (a *API) usageByThread(ctx context.Context, pid string, since time.Time, out *ProjectUsage) error {
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT t.no, COALESCE(t.title, ''), `+usageSums+`
		FROM at_usage u LEFT JOIN at_threads t ON t.id = u.thread_id WHERE u.project_id = $1 AND u.created_at >= $2
		GROUP BY t.no, t.title ORDER BY 3 DESC, t.no NULLS FIRST LIMIT 100`, pid, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t ThreadUsage
		if err := rows.Scan(&t.ThreadNo, &t.Title, &t.CostUSD, &t.Tokens.Input, &t.Tokens.Output, &t.Tokens.CacheRead, &t.Tokens.CacheWrite); err != nil {
			return err
		}
		out.ByThread = append(out.ByThread, t)
	}
	return rows.Err()
}

func (a *API) usageByEmployee(ctx context.Context, pid string, since time.Time, out *ProjectUsage) error {
	rows, err := a.e.Core.Pool.Query(ctx, `SELECT e.name, t.no, `+usageSums+`
		FROM at_usage u JOIN at_agent_sessions s ON s.id = u.session_id JOIN at_employees e ON e.id = s.employee_id
		LEFT JOIN at_threads t ON t.id = e.thread_id WHERE u.project_id = $1 AND u.created_at >= $2
		GROUP BY e.name, t.no ORDER BY 3 DESC, e.name LIMIT 200`, pid, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e ProjectEmployeeUsage
		if err := rows.Scan(&e.Employee, &e.ThreadNo, &e.CostUSD, &e.Tokens.Input, &e.Tokens.Output, &e.Tokens.CacheRead, &e.Tokens.CacheWrite); err != nil {
			return err
		}
		out.ByEmployee = append(out.ByEmployee, e)
	}
	return rows.Err()
}
