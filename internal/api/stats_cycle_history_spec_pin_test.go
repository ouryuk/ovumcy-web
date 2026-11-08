package api

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The cycle-history schema is the second spelling of CycleHistoryEntryResponse.
// Nothing in the route or status sweeps reads a property list, so a field
// added to the DTO alone would leave the response green here while a client
// validating against docs/openapi.yaml rejects the unexpected property. This
// pins the published schema to the struct in both directions, including
// `required`, because every field is always present on the wire.
func TestCycleHistoryEntrySpecEnumeratesExactlyTheDTOsFields(t *testing.T) {
	spec := readStatsOverviewSpecSchema(t, filepath.Join("..", "..", "docs", "openapi.yaml"), "CycleHistoryEntry")
	wire := jsonWireFields(t, CycleHistoryEntryResponse{})

	if len(spec.properties) == 0 {
		t.Fatal("CycleHistoryEntry declares no properties — this check is about a schema nobody read")
	}
	if spec.additionalProperties != "false" {
		t.Fatalf("CycleHistoryEntry declares additionalProperties: %q, want false", spec.additionalProperties)
	}
	if !reflect.DeepEqual(spec.properties, wire) {
		t.Fatalf("CycleHistoryEntry properties drifted from the DTO\n spec: %v\n  dto: %v", spec.properties, wire)
	}
	if !reflect.DeepEqual(spec.required, wire) {
		t.Fatalf("CycleHistoryEntry required drifted from the DTO\n spec: %v\n  dto: %v", spec.required, wire)
	}
}
