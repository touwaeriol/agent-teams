package office

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/touwaeriol/agent-teams/protocol"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

// MaxSize is the largest layout file accepted (office-layout.md §6.3).
const (
	MaxSize      = 512 << 10
	maxFurniture = 1000
)

// Built-in look keys every layout must keep (data-model.md §2.4).
var (
	requiredOutfits     = []string{"tee", "hoodie", "suit", "coat"}
	requiredAccessories = []string{"none", "glasses", "headset", "cap"}
)

// Problem is one validation error with a JSON pointer.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem found in a layout.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "布局无效"
	}
	return fmt.Sprintf("布局无效：%s %s", e.Problems[0].Path, e.Problems[0].Message)
}

// Validator checks resolved layouts against the shared JSON Schema plus the
// structural rules the backend owns (size, furniture count, required looks).
type Validator struct{ schema *protocol.Validator }

// NewValidator compiles the embedded schemas.
func NewValidator() (*Validator, error) {
	v, err := protocol.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Validator{schema: v}, nil
}

// Check validates one resolved layout document.
func (v *Validator) Check(doc []byte) error {
	if len(doc) > MaxSize {
		return &ValidationError{[]Problem{{"", fmt.Sprintf("文件超过 %d KB", MaxSize>>10)}}}
	}
	if err := v.schema.ValidateOffice(doc); err != nil {
		return &ValidationError{schemaProblems(err)}
	}
	return semantic(doc)
}

func schemaProblems(err error) []Problem {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{"", err.Error()}}
	}
	var out []Problem
	collect(ve, &out)
	if len(out) == 0 {
		out = append(out, Problem{"", ve.Error()})
	}
	return out
}

func collect(ve *jsonschema.ValidationError, out *[]Problem) {
	if len(ve.Causes) == 0 {
		*out = append(*out, Problem{"/" + joinPath(ve.InstanceLocation), ve.ErrorKind.LocalizedString(printer)})
		return
	}
	for _, c := range ve.Causes {
		collect(c, out)
	}
}

func joinPath(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "/"
		}
		s += p
	}
	return s
}

type lookKeys struct {
	Furniture []json.RawMessage `json:"furniture"`
	Looks     struct {
		Outfits     map[string]json.RawMessage `json:"outfits"`
		Accessories map[string]json.RawMessage `json:"accessories"`
	} `json:"looks"`
}

func semantic(doc []byte) error {
	var d lookKeys
	if err := json.Unmarshal(doc, &d); err != nil {
		return &ValidationError{[]Problem{{"", err.Error()}}}
	}
	var problems []Problem
	if len(d.Furniture) > maxFurniture {
		problems = append(problems, Problem{"/furniture", fmt.Sprintf("家具最多 %d 件", maxFurniture)})
	}
	problems = append(problems, missing("/looks/outfits", d.Looks.Outfits, requiredOutfits)...)
	problems = append(problems, missing("/looks/accessories", d.Looks.Accessories, requiredAccessories)...)
	if len(problems) > 0 {
		return &ValidationError{problems}
	}
	return nil
}

func missing(path string, have map[string]json.RawMessage, want []string) []Problem {
	var out []Problem
	for _, k := range want {
		if _, ok := have[k]; !ok {
			out = append(out, Problem{path, "缺少内置的 " + k})
		}
	}
	return out
}

// Looks are the outfit/accessory keys and colours of a layout (Team page, role validation).
type Looks struct {
	Outfits     []string
	Accessories []string
	Colors      []string
	Hair        []string
	Defaults    map[string]LookDefault // by role, tier or "*"
}

// LookDefault is one entry of looks.defaults.
type LookDefault struct {
	Color     string  `json:"color"`
	Outfit    string  `json:"outfit"`
	Accessory string  `json:"accessory"`
	Hair      *string `json:"hair"`
}

// LooksOf extracts the look keys of a resolved layout.
func LooksOf(doc []byte) Looks {
	var d struct {
		Looks struct {
			Outfits     map[string]json.RawMessage `json:"outfits"`
			Accessories map[string]json.RawMessage `json:"accessories"`
			Colors      []string                   `json:"colors"`
			Hair        []string                   `json:"hair"`
			Defaults    map[string]LookDefault     `json:"defaults"`
		} `json:"looks"`
	}
	_ = json.Unmarshal(doc, &d)
	return Looks{Outfits: keys(d.Looks.Outfits), Accessories: keys(d.Looks.Accessories),
		Colors: d.Looks.Colors, Hair: d.Looks.Hair, Defaults: d.Looks.Defaults}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
