package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ovumcy/ovumcy-web/internal/models"
)

// ImportCSV restores a prior CSV export back into the owner's account. It is
// the inverse of GET /api/v1/exports/csv: the header row must name exactly the
// documented export columns (in any order, case- and spacing-insensitively),
// and every cell is re-read through the same normalization the JSON restore
// applies. The write path is shared with ImportJSON, so a CSV restore is just
// as additive: days the account already has are skipped untouched, and nothing
// is ever overwritten or deleted.
//
// A row whose Date is missing or invalid is rejected and counted; a duplicate
// calendar day inside the file is rejected the same way. Every other field is
// lenient per cell, matching the documented JSON restore contract: an
// out-of-vocabulary label becomes its neutral value and the rest of the day
// still imports.
func (service *ImportService) ImportCSV(ctx context.Context, userID uint, raw []byte, location *time.Location) (ImportResult, error) {
	if location == nil {
		location = time.UTC
	}

	entries, err := parseImportCSV(raw)
	if err != nil {
		return ImportResult{}, err
	}

	return service.restore(ctx, userID, entries, location)
}

// importCSVHeaderCanonical maps a normalized header spelling onto the canonical
// export header it names. It is derived from ExportCSVHeaders so the accepted
// file shape cannot drift from what the exporter writes.
var importCSVHeaderCanonical = buildImportCSVHeaderCanonical()

func buildImportCSVHeaderCanonical() map[string]string {
	headers := ExportCSVHeaders()
	canonical := make(map[string]string, len(headers))
	for _, header := range headers {
		canonical[normalizeImportCSVHeader(header)] = header
	}
	return canonical
}

func normalizeImportCSVHeader(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// parseImportCSV reads the whole file into the export entry shape. Transport or
// shape problems (bad quoting, the wrong number of cells on a row, a header
// that is not exactly the documented set) map onto ErrImportMalformed, while a
// file carrying more rows than MaxImportEntries maps onto ErrImportTooLarge —
// the same two keys the JSON restore uses.
func parseImportCSV(raw []byte) ([]ExportJSONEntry, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	reader := csv.NewReader(bytes.NewReader(raw))
	reader.ReuseRecord = false

	header, err := reader.Read()
	if err != nil {
		return nil, ErrImportMalformed
	}
	columns, err := importCSVColumns(header)
	if err != nil {
		return nil, err
	}
	// Every record must carry the header's cell count. Left at the reader's
	// default (0), a short row would be padded into ambiguous positions.
	reader.FieldsPerRecord = len(header)

	entries := make([]ExportJSONEntry, 0)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrImportMalformed
		}
		if len(entries) >= MaxImportEntries {
			return nil, ErrImportTooLarge
		}
		entries = append(entries, importCSVEntry(record, columns))
	}
	return entries, nil
}

// importCSVColumns resolves the header row to canonical column positions. The
// header must name every documented export column exactly once and nothing
// else, so a file from another product, a truncated header, or a duplicated
// column is refused before any row is trusted.
func importCSVColumns(header []string) (map[string]int, error) {
	expected := ExportCSVHeaders()
	if len(header) != len(expected) {
		return nil, ErrImportMalformed
	}

	columns := make(map[string]int, len(expected))
	for position, name := range header {
		canonical, ok := importCSVHeaderCanonical[normalizeImportCSVHeader(name)]
		if !ok {
			return nil, ErrImportMalformed
		}
		if _, duplicate := columns[canonical]; duplicate {
			return nil, ErrImportMalformed
		}
		columns[canonical] = position
	}
	return columns, nil
}

// importCSVEntry maps one record onto the export entry shape the shared restore
// planner consumes. The column set is already known to be complete, so a direct
// lookup cannot miss.
func importCSVEntry(record []string, columns map[string]int) ExportJSONEntry {
	cell := func(header string) string {
		return record[columns[header]]
	}

	return ExportJSONEntry{
		Date:          strings.TrimSpace(cell("Date")),
		Period:        importCSVYesNo(cell("Period")),
		CycleStart:    importCSVYesNo(cell("Cycle start")),
		IsUncertain:   importCSVYesNo(cell("Uncertain")),
		Flow:          NormalizeDayFlow(cell("Flow")),
		MoodRating:    importCSVMood(cell("Mood rating")),
		SexActivity:   NormalizeDaySexActivity(cell("Sex activity")),
		BBT:           importCSVBBT(cell("BBT (C)")),
		CervicalMucus: importCSVCervicalMucus(cell("Cervical mucus")),
		PregnancyTest: NormalizeDayPregnancyTest(cell("Pregnancy test")),
		CycleFactors:  importCSVCycleFactorKeys(cell("Cycle factors")),
		Symptoms: ExportSymptomFlags{
			Cramps:           importCSVYesNo(cell("Cramps")),
			Headache:         importCSVYesNo(cell("Headache")),
			Acne:             importCSVYesNo(cell("Acne")),
			Mood:             importCSVYesNo(cell("Mood")),
			Bloating:         importCSVYesNo(cell("Bloating")),
			Fatigue:          importCSVYesNo(cell("Fatigue")),
			BreastTenderness: importCSVYesNo(cell("Breast tenderness")),
			BackPain:         importCSVYesNo(cell("Back pain")),
			Nausea:           importCSVYesNo(cell("Nausea")),
			Spotting:         importCSVYesNo(cell("Spotting")),
			Irritability:     importCSVYesNo(cell("Irritability")),
			Insomnia:         importCSVYesNo(cell("Insomnia")),
			FoodCravings:     importCSVYesNo(cell("Food cravings")),
			Diarrhea:         importCSVYesNo(cell("Diarrhea")),
			Constipation:     importCSVYesNo(cell("Constipation")),
			Swelling:         importCSVYesNo(cell("Swelling")),
		},
		OtherSymptoms: importCSVList(cell("Other")),
		Notes:         unsanitizeImportCSVTextCell(cell("Notes")),
	}
}

func importCSVYesNo(cell string) bool {
	return strings.EqualFold(strings.TrimSpace(cell), "yes")
}

func importCSVMood(cell string) int {
	value, err := strconv.Atoi(strings.TrimSpace(cell))
	if err != nil {
		return 0
	}
	return normalizeExportMood(value)
}

func importCSVBBT(cell string) *float64 {
	raw := strings.TrimSpace(cell)
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &value
}

// importCSVCervicalMucus translates the one export label that is not already
// the enum spelling ("Egg white" -> "eggwhite") and defers the rest to the
// shared normalizer.
func importCSVCervicalMucus(cell string) string {
	if strings.EqualFold(strings.TrimSpace(cell), "egg white") {
		return models.CervicalMucusEggWhite
	}
	return NormalizeDayCervicalMucus(cell)
}

var importCSVCycleFactorKeysByLabel = map[string]string{
	"stress":            models.CycleFactorStress,
	"illness":           models.CycleFactorIllness,
	"travel":            models.CycleFactorTravel,
	"sleep disruption":  models.CycleFactorSleepDisruption,
	"medication change": models.CycleFactorMedicationChange,
}

// importCSVCycleFactorKeys translates the human-readable labels the exporter
// writes back into the closed key catalog. An unrecognized label is passed
// through, where the shared day normalizer drops it — exactly what the JSON
// restore does with an unknown factor key.
func importCSVCycleFactorKeys(cell string) []string {
	cell = unsanitizeImportCSVTextCell(cell)
	parts := strings.Split(cell, ";")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.ToLower(strings.TrimSpace(part))
		if label == "" {
			continue
		}
		if key, ok := importCSVCycleFactorKeysByLabel[label]; ok {
			keys = append(keys, key)
			continue
		}
		keys = append(keys, label)
	}
	return keys
}

// importCSVList splits a "; "-joined free-text cell (custom symptom names) and
// drops empty segments.
func importCSVList(cell string) []string {
	cell = unsanitizeImportCSVTextCell(cell)
	parts := strings.Split(cell, ";")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

// unsanitizeImportCSVTextCell reverses the export's CSV-injection mitigation:
// a leading apostrophe is removed only when what follows is exactly a prefix
// the exporter would have neutralized, so a genuine note that starts with an
// apostrophe is left alone. Without this, exporting and re-importing a note
// such as "=cmd" would store the protective apostrophe as user text.
func unsanitizeImportCSVTextCell(value string) string {
	if strings.HasPrefix(value, "'") && hasDangerousCSVPrefix(value[1:]) {
		return value[1:]
	}
	return value
}
