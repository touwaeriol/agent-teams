package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/touwaeriol/agent-teams/server/internal/office"
)

type layoutInput struct {
	ID          string `path:"id" maxLength:"64"`
	IfNoneMatch string `header:"If-None-Match"`
}

type currentLayoutInput struct {
	Project     string `query:"project" doc:"Use this project's layout setting"`
	IfNoneMatch string `header:"If-None-Match"`
}

type layoutOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	LayoutID     string `header:"X-Layout-Id"`
	CacheControl string `header:"Cache-Control"`
	ContentType  string `header:"Content-Type"`
	Body         []byte `contentType:"application/json" doc:"Office layout JSON (office-layout.schema.json)"`
}

type layoutStatusesOutput struct{ Body []office.Status }

type putLayoutInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Doc json.RawMessage `json:"doc" doc:"Layout document (office-layout.schema.json)"`
	}
}

type layoutStatusOutput struct{ Body office.Status }

type layoutIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

func (s *Server) registerOffice(api huma.API, r chi.Router) {
	huma.Register(api, huma.Operation{OperationID: "get-office-layout", Method: "GET", Path: "/api/office/layouts/{id}",
		Tags: []string{"office"}, Summary: "A layout's JSON (ETag = content hash)"}, s.getLayout)
	huma.Register(api, huma.Operation{OperationID: "get-current-office-layout", Method: "GET", Path: "/api/office/layout",
		Tags: []string{"office"}, Summary: "The layout in effect: project setting, then global default, then default"}, s.getCurrentLayout)
	huma.Register(api, huma.Operation{OperationID: "admin-list-office-layouts", Method: "GET", Path: "/api/admin/office/layouts",
		Tags: []string{"admin"}}, s.listLayouts)
	huma.Register(api, huma.Operation{OperationID: "admin-put-office-layout", Method: "PUT", Path: "/api/admin/office/layouts/{id}",
		Tags: []string{"admin"}, Summary: "Validate and store a database layout (overrides the file of the same id)"}, s.putLayout)
	huma.Register(api, huma.Operation{OperationID: "admin-delete-office-layout", Method: "DELETE", Path: "/api/admin/office/layouts/{id}",
		Tags: []string{"admin"}, DefaultStatus: 204}, s.deleteLayout)
}

func (s *Server) getLayout(ctx context.Context, in *layoutInput) (*layoutOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	return s.layoutResponse(in.ID, in.IfNoneMatch)
}

func (s *Server) getCurrentLayout(ctx context.Context, in *currentLayoutInput) (*layoutOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	id := s.officeDefault(ctx)
	if in.Project != "" {
		project, err := s.store.ProjectOf(ctx, p.User.ID, in.Project)
		if err != nil {
			return nil, storeErr(err, "没有这个项目")
		}
		id = first(project.OfficeLayout, id)
	}
	if _, err := s.office.Get(id); err != nil {
		id = office.DefaultID
	}
	return s.layoutResponse(id, in.IfNoneMatch)
}

func (s *Server) layoutResponse(id, ifNoneMatch string) (*layoutOutput, error) {
	layout, err := s.office.Get(id)
	if err != nil {
		return nil, fail(http.StatusNotFound, "layout_not_found", "没有这个布局")
	}
	etag := `"` + layout.Version + `"`
	out := &layoutOutput{Status: http.StatusOK, ETag: etag, LayoutID: layout.ID, CacheControl: "no-cache",
		ContentType: "application/json", Body: layout.Doc}
	if strings.Contains(ifNoneMatch, etag) {
		out.Status, out.Body, out.ContentType = http.StatusNotModified, nil, ""
	}
	return out, nil
}

func (s *Server) listLayouts(ctx context.Context, _ *struct{}) (*layoutStatusesOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return &layoutStatusesOutput{Body: s.office.List()}, nil
}

func (s *Server) putLayout(ctx context.Context, in *putLayoutInput) (*layoutStatusOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	layout, err := s.office.Put(ctx, in.ID, in.Body.Doc, "user:"+admin.User.ID)
	if err != nil {
		return nil, layoutErr(err)
	}
	s.audit(ctx, admin.User.ID, "office_layout_save", "office_layout", in.ID, nil)
	at := layout.UpdatedAt
	return &layoutStatusOutput{Body: office.Status{ID: layout.ID, Source: layout.Source, Version: layout.Version,
		UpdatedAt: &at, Valid: true}}, nil
}

func layoutErr(err error) error {
	var ve *office.ValidationError
	if errors.As(err, &ve) {
		fields := make([]FieldError, len(ve.Problems))
		for i, p := range ve.Problems {
			fields[i] = FieldError{Path: "doc" + p.Path, Message: p.Message}
		}
		return invalid(fields, ve.Error())
	}
	if errors.Is(err, office.ErrBadID) {
		return fail(http.StatusUnprocessableEntity, "validation", err.Error())
	}
	return err
}

func (s *Server) deleteLayout(ctx context.Context, in *layoutIDInput) (*struct{}, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.office.Delete(ctx, in.ID); err != nil {
		if errors.Is(err, office.ErrNotFound) {
			return nil, fail(http.StatusNotFound, "layout_not_found", "数据库里没有这个布局（文件布局只能改文件）")
		}
		return nil, err
	}
	s.audit(ctx, admin.User.ID, "office_layout_delete", "office_layout", in.ID, nil)
	return nil, nil
}

// layoutChanged fans a layout event out to every project using the layout.
func (s *Server) layoutChanged(id, version string) {
	ctx := context.Background()
	isDefault := id == s.officeDefault(ctx)
	ids, err := s.store.ProjectIDsUsingLayout(ctx, id, isDefault)
	if err != nil {
		s.log.Warn("layout fan-out", "id", id, "err", err)
		return
	}
	for _, pid := range ids {
		s.events.Project(ctx, pid, "layout", map[string]string{"layout_id": id, "version": version})
	}
}

// serveOfficeFile serves /office/<id>.json (resolved layout) and /office/assets/* (OFFICE_DIR).
func (s *Server) serveOfficeFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "*")
	if strings.HasPrefix(name, "assets/") {
		s.serveOfficeAsset(w, r, strings.TrimPrefix(name, "assets/"))
		return
	}
	layout, err := s.office.Get(strings.TrimSuffix(name, ".json"))
	if !strings.HasSuffix(name, ".json") || err != nil {
		writeError(w, fail(http.StatusNotFound, "not_found", "没有找到"))
		return
	}
	etag := `"` + layout.Version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(layout.Doc)
}

func (s *Server) serveOfficeAsset(w http.ResponseWriter, r *http.Request, rel string) {
	root := filepath.Join(s.cfg.OfficeDir, "assets")
	path := filepath.Join(root, filepath.Clean("/"+rel))
	info, err := os.Stat(path)
	if s.cfg.OfficeDir == "" || err != nil || info.IsDir() {
		writeError(w, fail(http.StatusNotFound, "not_found", "没有找到"))
		return
	}
	http.ServeFile(w, r, path)
}
