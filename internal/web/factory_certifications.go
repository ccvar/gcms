package web

import (
	"encoding/json"
	"strings"
)

const (
	factoryCertificationsSettingKey = "factory.certifications"
	maxFactoryCertifications        = 8
)

// FactoryCertification is one verified certification or audit supplied by the
// site owner.  There are deliberately no built-in examples: a missing setting
// means the certification strip is not rendered.
type FactoryCertification struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// parseFactoryCertifications accepts the site setting representation, drops
// empty rows, and caps output so a malformed payload cannot overwhelm a hero.
func parseFactoryCertifications(raw string) []FactoryCertification {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	out := make([]FactoryCertification, 0, min(len(rows), maxFactoryCertifications))
	for _, row := range rows {
		name := scalarString(row["name"])
		if name == "" {
			continue
		}
		out = append(out, FactoryCertification{
			Name: name,
			Note: scalarString(row["note"]),
		})
		if len(out) == maxFactoryCertifications {
			break
		}
	}
	return out
}
