package protocol

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// IDBase is the $id prefix of every schema; "<IDBase>node/hello.json" etc.
const IDBase = "https://agent-teams.dev/schemas/"

//go:embed schemas manifest.json
var embedded embed.FS

// SchemaFS exposes the embedded schema tree (node/, tools/, office/).
func SchemaFS() fs.FS {
	sub, err := fs.Sub(embedded, "schemas")
	if err != nil {
		panic(err)
	}
	return sub
}

// Validator validates frames, tool inputs and office layouts against the embedded schemas.
type Validator struct {
	message *jsonschema.Schema
	office  *jsonschema.Schema
	tools   map[string]*jsonschema.Schema
}

// NewValidator compiles the embedded schemas (formats are asserted).
func NewValidator() (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := addResources(c); err != nil {
		return nil, err
	}
	v := &Validator{tools: map[string]*jsonschema.Schema{}}
	var err error
	if v.message, err = c.Compile(IDBase + "node/message.json"); err != nil {
		return nil, err
	}
	if v.office, err = c.Compile(officeID); err != nil {
		return nil, err
	}
	for name := range Tools {
		if v.tools[name], err = c.Compile(IDBase + "tools/" + name + ".json"); err != nil {
			return nil, err
		}
	}
	return v, nil
}

const officeID = "https://agent-teams.dev/schemas/office/office-layout.schema.json"

func addResources(c *jsonschema.Compiler) error {
	return fs.WalkDir(SchemaFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		doc, err := readDoc(path)
		if err != nil {
			return err
		}
		return c.AddResource(IDBase+path, doc)
	})
}

func readDoc(path string) (any, error) {
	raw, err := fs.ReadFile(SchemaFS(), path)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// ValidateFrame checks one WebSocket frame (envelope + body selected by type).
func (v *Validator) ValidateFrame(frame []byte) error {
	return validateRaw(v.message, frame)
}

// ValidateToolInput checks tool.call args against the tool's input schema.
func (v *Validator) ValidateToolInput(tool string, args []byte) error {
	s, ok := v.tools[tool]
	if !ok {
		return fmt.Errorf("unknown platform tool %q", tool)
	}
	return validateRaw(s, args)
}

// ValidateOffice checks an office layout document.
func (v *Validator) ValidateOffice(layout []byte) error {
	return validateRaw(v.office, layout)
}

func validateRaw(s *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// ToolInputSchema returns the self-contained input schema of a tool, as sent in
// session.open platform_tools[].input_schema.
func ToolInputSchema(tool string) (json.RawMessage, error) {
	specs, err := toolSpecs()
	if err != nil {
		return nil, err
	}
	spec, ok := specs[tool]
	if !ok {
		return nil, fmt.Errorf("unknown platform tool %q", tool)
	}
	return spec.InputSchema, nil
}

// ToolDescription returns the tool description shown to the model.
func ToolDescription(tool string) (string, error) {
	specs, err := toolSpecs()
	if err != nil {
		return "", err
	}
	spec, ok := specs[tool]
	if !ok {
		return "", fmt.Errorf("unknown platform tool %q", tool)
	}
	return spec.Description, nil
}

type manifestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func toolSpecs() (map[string]manifestTool, error) {
	raw, err := embedded.ReadFile("manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		Tools []manifestTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := make(map[string]manifestTool, len(m.Tools))
	for _, t := range m.Tools {
		out[t.Name] = t
	}
	return out, nil
}

// PlatformTools returns the session.open platform_tools for a tier, sorted by name.
// flags lists the role flags that are on (e.g. "can_delegate").
func PlatformTools(tier Tier, flags ...string) ([]PlatformToolSpec, error) {
	specs, err := toolSpecs()
	if err != nil {
		return nil, err
	}
	var out []PlatformToolSpec
	for _, name := range sortedToolNames() {
		meta := Tools[name]
		if !meta.AvailableTo(tier) || !hasFlag(flags, meta.Requires) {
			continue
		}
		spec := specs[name]
		out = append(out, PlatformToolSpec{Name: name, Description: spec.Description, InputSchema: spec.InputSchema, Blocking: meta.BlocksFor(tier)})
	}
	return out, nil
}

func hasFlag(flags []string, want string) bool {
	return want == "" || slices.Contains(flags, want)
}

func sortedToolNames() []string {
	names := make([]string, 0, len(Tools))
	for name := range Tools {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
