package office

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestStaffAreaIsOptional: the layout from before shared members (no staffArea, members seated
// in the thread pods; the web keeps it as a test fixture) still validates, the default has a
// staff area, and a misspelled staffArea field is rejected like any other.
func TestStaffAreaIsOptional(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile("../../../web/src/office/__tests__/legacy-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), `"staffArea"`) {
		t.Fatal("the legacy fixture should not have a staff area")
	}
	if err := v.Check(legacy); err != nil {
		t.Fatalf("legacy layout rejected: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(embeddedDefault, &doc); err != nil {
		t.Fatal(err)
	}
	staff, ok := doc["staffArea"].(map[string]any)
	if !ok {
		t.Fatal("default layout has no staffArea")
	}
	staff["perRoww"] = 3
	raw, _ := json.Marshal(doc)
	if err := v.Check(raw); err == nil || !strings.Contains(err.Error(), "staffArea") {
		t.Fatalf("misspelled staffArea field: %v", err)
	}
}
