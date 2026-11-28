package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/skills"
)

// SkillsBody is GET /api/skills.
type SkillsBody struct {
	Skills []skills.Skill `json:"skills"`
}

type skillsOutput struct{ Body SkillsBody }

type skillOutput struct{ Body skills.Skill }

type newSkillInput struct {
	Body struct {
		Zip   string        `json:"zip,omitempty" maxLength:"2900000" doc:"Base64 of a zip of the skill folder (SKILL.md at the root or in one top folder)"`
		Files []skills.File `json:"files,omitempty" maxItems:"64" doc:"The skill's text files (a picked folder); used when zip is empty"`
	}
}

type skillIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

func (s *Server) registerSkills(api huma.API) {
	tags := []string{"team"}
	huma.Register(api, huma.Operation{OperationID: "list-skills", Method: "GET", Path: "/api/skills", Tags: tags,
		Summary: "Built-in and uploaded skills"}, s.listSkills)
	huma.Register(api, huma.Operation{OperationID: "create-skill", Method: "POST", Path: "/api/skills", Tags: tags,
		DefaultStatus: http.StatusCreated, MaxBodyBytes: 4 << 20,
		Summary: "Upload a skill (zip or text files; SKILL.md front matter gives name and description)"}, s.createSkill)
	huma.Register(api, huma.Operation{OperationID: "delete-skill", Method: "DELETE", Path: "/api/skills/{id}", Tags: tags,
		DefaultStatus: http.StatusNoContent, Summary: "Delete an uploaded skill and remove it from the roles"}, s.deleteSkill)
}

func (s *Server) listSkills(ctx context.Context, _ *struct{}) (*skillsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := skills.List(ctx, s.store.Pool, p.User.ID)
	return &skillsOutput{Body: SkillsBody{Skills: list}}, err
}

func (s *Server) createSkill(ctx context.Context, in *newSkillInput) (*skillOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	files, err := uploadedFiles(in.Body.Zip, in.Body.Files)
	if err != nil {
		return nil, skillError(err)
	}
	sk, err := skills.Create(ctx, s.store.Pool, p.User.ID, files)
	if err != nil {
		return nil, skillError(err)
	}
	s.audit(ctx, p.User.ID, "skill_create", "skill", sk.ID, map[string]string{"name": sk.Name})
	return &skillOutput{Body: sk}, nil
}

func uploadedFiles(zipB64 string, files []skills.File) ([]skills.File, error) {
	if zipB64 == "" {
		return skills.StripCommonDir(files), nil
	}
	raw, err := base64.StdEncoding.DecodeString(zipB64)
	if err != nil {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "zip 不是有效的 base64")
	}
	return skills.FromZip(raw)
}

func skillError(err error) error {
	if skills.IsProblem(err) {
		return failErr(http.StatusUnprocessableEntity, "validation", err)
	}
	return err
}

func (s *Server) deleteSkill(ctx context.Context, in *skillIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	err = skills.Delete(ctx, s.store.Pool, p.User.ID, in.ID)
	if errors.Is(err, skills.ErrNotFound) {
		return nil, fail(http.StatusNotFound, "not_found", "没有这个技能（内置技能不能删除）")
	}
	return nil, err
}
