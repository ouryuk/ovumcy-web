package api

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
)

// cycleHistoryCSVFormat is the value of the `format` query parameter that asks
// GET /api/v1/stats/cycles for a CSV download instead of the JSON array.
const cycleHistoryCSVFormat = "csv"

// cycleHistoryCSVEgress tags the per-cycle CSV download as audited
// health-data egress, exactly like the account export. It is a separate
// action record rather than a reuse of exportCSVEgress so the audit line
// names the scope that actually left the instance: the export_format detail
// is "stats_cycles", and an operator filtering on the account export's
// "csv" does not also count a cycle-history hand-off.
var cycleHistoryCSVEgress = healthEgressKind{
	action: dataExportAction,
	target: exportEgressTarget,
	detail: securityEventField("export_format", "stats_cycles"),
}

// cycleHistoryCSVStatus values for the ovulation_source column. The JSON DTO
// carries two booleans; a spreadsheet reader needs one word, and "none" keeps
// the column populated when ovulation_date is empty rather than leaving the
// reader to infer it.
const (
	cycleHistoryCSVStatusNone      = "none"
	cycleHistoryCSVStatusConfirmed = "confirmed"
	cycleHistoryCSVStatusEstimated = "estimated"
)

// cycleHistoryCSVHeaders are fixed English column names, like the account
// export's: the file is for journals and clinician hand-off, so it must read
// the same whatever language the owner's session is in. The columns are the
// stats page table's, in its order.
func cycleHistoryCSVHeaders() []string {
	return []string{"start_date", "end_date", "length_days", "ovulation_date", "ovulation_source"}
}

// cycleHistoryCSVRow maps one DTO entry onto its wire row. It reads only the
// published DTO, so the file and the JSON array cannot describe a cycle
// differently; a missing ovulation day is an empty cell.
func cycleHistoryCSVRow(entry CycleHistoryEntryResponse) []string {
	ovulationDate := ""
	if entry.OvulationDate != nil {
		ovulationDate = *entry.OvulationDate
	}
	status := cycleHistoryCSVStatusNone
	switch {
	case entry.OvulationConfirmed:
		status = cycleHistoryCSVStatusConfirmed
	case entry.OvulationEstimated:
		status = cycleHistoryCSVStatusEstimated
	}
	return []string{entry.StartDate, entry.EndDate, strconv.Itoa(entry.Length), ovulationDate, status}
}

// buildCycleHistoryCSV renders the entries as CSV, oldest cycle first, with
// one row per cycle. encoding/csv does the quoting, so a value carrying the
// separator or a quote cannot break the column layout. The returned bytes end
// with a newline, and an empty history is the header row alone.
func buildCycleHistoryCSV(entries []CycleHistoryEntryResponse) ([]byte, error) {
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(cycleHistoryCSVHeaders()); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := writer.Write(cycleHistoryCSVRow(entry)); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// sendCycleHistoryCSV answers with the CSV attachment and audits the egress.
func (handler *Handler) sendCycleHistoryCSV(c fiber.Ctx, entries []CycleHistoryEntryResponse, now time.Time) error {
	document, err := buildCycleHistoryCSV(entries)
	if err != nil {
		return handler.failEgress(c, cycleHistoryCSVEgress, exportBuildErrorSpec()) // codecov:ignore -- unreachable: bytes.Buffer writes cannot fail
	}
	// A name of its own, so a cycle-history download never sits beside an
	// account export under the same filename.
	setExportAttachmentHeaders(c, "text/csv", fmt.Sprintf("ovumcy-cycle-history-%s.csv", now.Format(statsOverviewDateLayout)))
	handler.logEgressSuccess(c, cycleHistoryCSVEgress)
	return c.Send(document)
}
