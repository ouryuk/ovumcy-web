package api

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The briefing schemas are the spelling of the report DTO. Nothing in the
// route or status sweeps reads a property list, so a field added to the DTO
// alone would leave the response green here while a client validating against
// docs/openapi.yaml rejects the unexpected property. This pins both published
// schemas to their structs in both directions, including `required`, because
// every field is always present on the wire.
func TestAppointmentBriefingSpecsEnumerateExactlyTheDTOsFields(t *testing.T) {
	specPath := filepath.Join("..", "..", "docs", "openapi.yaml")

	t.Run("AppointmentBriefing", func(t *testing.T) {
		spec := readStatsOverviewSpecSchema(t, specPath, "AppointmentBriefing")
		wire := jsonWireFields(t, AppointmentBriefingResponse{})

		if len(spec.properties) == 0 {
			t.Fatal("AppointmentBriefing declares no properties — this check is about a schema nobody read")
		}
		if spec.additionalProperties != "false" {
			t.Fatalf("AppointmentBriefing declares additionalProperties: %q, want false", spec.additionalProperties)
		}
		if !reflect.DeepEqual(spec.properties, wire) {
			t.Fatalf("AppointmentBriefing properties drifted from the DTO\n spec: %v\n  dto: %v", spec.properties, wire)
		}
		if !reflect.DeepEqual(spec.required, wire) {
			t.Fatalf("AppointmentBriefing required drifted from the DTO\n spec: %v\n  dto: %v", spec.required, wire)
		}
	})

	t.Run("BriefingSymptomCount", func(t *testing.T) {
		spec := readStatsOverviewSpecSchema(t, specPath, "BriefingSymptomCount")
		wire := jsonWireFields(t, BriefingSymptomCountResponse{})

		if len(spec.properties) == 0 {
			t.Fatal("BriefingSymptomCount declares no properties — this check is about a schema nobody read")
		}
		if spec.additionalProperties != "false" {
			t.Fatalf("BriefingSymptomCount declares additionalProperties: %q, want false", spec.additionalProperties)
		}
		if !reflect.DeepEqual(spec.properties, wire) {
			t.Fatalf("BriefingSymptomCount properties drifted from the DTO\n spec: %v\n  dto: %v", spec.properties, wire)
		}
		if !reflect.DeepEqual(spec.required, wire) {
			t.Fatalf("BriefingSymptomCount required drifted from the DTO\n spec: %v\n  dto: %v", spec.required, wire)
		}
	})
}
