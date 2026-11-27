package api

import (
	"log"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/ovumcy/ovumcy-web/internal/models"
	"github.com/ovumcy/ovumcy-web/internal/services"
)

func (handler *Handler) buildSettingsViewData(c fiber.Ctx, user *models.User, flash FlashPayload) (fiber.Map, error) {
	messages := currentMessages(c)
	language := currentLanguage(c)
	location := handler.requestLocation(c)

	viewData, err := handler.settingsViewService.BuildSettingsPageViewData(
		c.Context(),
		user,
		language,
		services.SettingsViewInput{
			FlashSuccess: flash.SettingsSuccess,
			FlashError:   flash.SettingsError,
		},
		time.Now().In(location),
		location,
	)
	if err != nil {
		return nil, err
	}

	*user = viewData.CurrentUser

	data := fiber.Map{
		"Title":       localizedPageTitle(messages, "meta.title.settings", "Ovumcy | Settings"),
		"CurrentUser": user,
		// OIDCEnabled gates the "link an OIDC identity" card (issue #701): the
		// step-up it starts (StartOIDCIdentityLinkStepup) refuses immediately
		// when the provider is disabled, so hiding the control when it can only
		// ever fail is a display decision, not a second copy of that check.
		"OIDCEnabled":            handler.oidcService != nil && handler.oidcService.Enabled(),
		"ErrorKey":               viewData.ErrorKey,
		"ChangePasswordErrorKey": viewData.ChangePasswordErrorKey,
		"SuccessKey":             viewData.SuccessKey,
		"CycleLength":            viewData.CycleLength,
		"PeriodLength":           viewData.PeriodLength,
		"AutoPeriodFill":         viewData.AutoPeriodFill,
		"IrregularCycle":         viewData.IrregularCycle,
		"UnpredictableCycle":     viewData.UnpredictableCycle,
		"AgeGroup":               viewData.AgeGroup,
		"UsageGoal":              viewData.UsageGoal,
		"UsageGoalLabelKey":      services.UsageGoalTranslationKey(viewData.UsageGoal),
		"UsageGoalSummaryKey":    services.UsageGoalSummaryTranslationKey(viewData.UsageGoal),
		"ShownPeriodTip":         viewData.ShownPeriodTip,
		"TrackBBT":               viewData.TrackBBT,
		"TemperatureUnit":        viewData.TemperatureUnit,
		"TrackCervicalMucus":     viewData.TrackCervicalMucus,
		"ShowSexChip":            viewData.ShowSexChip,
		"ShowCycleFactors":       viewData.ShowCycleFactors,
		"ShowNotesField":         viewData.ShowNotesField,
		"ShowHistoricalPhases":   viewData.ShowHistoricalPhases,
		"WeekStartsOn":           viewData.WeekStartsOn,
		"ReminderLeadDays":       viewData.ReminderLeadDays,
		"LastPeriodStart":        viewData.LastPeriodStart,
		"TodayISO":               viewData.TodayISO,
		"CycleStartMinISO":       viewData.CycleStartMinISO,
	}

	// The egress account is projected only past the owner gate, and as ONE key.
	// The six flat keys it replaced were set unconditionally: a session that was
	// not this row's owner received the endpoint's configured flag, its host, and
	// the feed's configured flag as FALSE values rather than as absent ones, and a
	// false value is still an answer to a question that session may not ask.
	// Regression: TestSettingsViewDataNamesNoEgressKeyForANonOwner.
	if viewData.HasOwnerEgressLedger {
		data["Egress"] = buildSettingsEgressView(c, viewData.Egress, location)
	}

	// The delivery history is projected the same way: past the owner gate, as
	// ONE key, so a non-owner session receives an absent key rather than a
	// false one.
	//
	// The read is BEST EFFORT here too, and deliberately so: a history that
	// cannot be read must not take the Settings page down with it. The list is
	// evidence about past deliveries, and a page that renders without it is a
	// page with one thing missing — a far better outcome than a 500 on a
	// settings page that also carries the password change and the data wipe.
	if handler.webhookDeliveryHist != nil {
		deliveries, err := handler.webhookDeliveryHist.List(c.Context(), user.ID, services.WebhookDeliveryRingSize)
		if err != nil {
			log.Printf("settings view: webhook delivery history read failed, owner id=%d", user.ID)
		} else {
			data["WebhookDeliveries"] = buildSettingsWebhookDeliveriesView(language, location, deliveries)
		}
	}

	if viewData.HasOwnerExportViewState {
		data["ExportTotalEntries"] = viewData.Export.SummaryTotalEntries
		data["HasExportData"] = viewData.Export.HasData
		data["HasExportSummaryData"] = viewData.Export.SummaryHasData
		data["ExportDateFrom"] = viewData.Export.DefaultDateFrom
		data["ExportDateTo"] = viewData.Export.DefaultDateTo
		data["ExportRangeMin"] = viewData.Export.SelectableDateMin
		data["ExportRangeMax"] = viewData.Export.SelectableDateMax
		data["ExportSummaryDateFrom"] = viewData.Export.SummaryDateFrom
		data["ExportSummaryDateTo"] = viewData.Export.SummaryDateTo
		data["ExportDateFromDisplay"] = viewData.Export.SummaryDateFromDisplay
		data["ExportDateToDisplay"] = viewData.Export.SummaryDateToDisplay
	}

	if viewData.HasOwnerSymptomsView {
		data["ActiveCustomSymptoms"] = buildSettingsSymptomRows(viewData.Symptoms.ActiveCustomSymptoms, settingsSymptomRowState{}, func(source string) string {
			return localizedSettingsSymptomStatus(c, source)
		}, func(source string) string {
			return localizedSettingsSymptomError(c, source)
		})
		data["ArchivedCustomSymptoms"] = buildSettingsSymptomRows(viewData.Symptoms.ArchivedCustomSymptoms, settingsSymptomRowState{}, func(source string) string {
			return localizedSettingsSymptomStatus(c, source)
		}, func(source string) string {
			return localizedSettingsSymptomError(c, source)
		})
		data["HasCustomSymptoms"] = viewData.Symptoms.HasCustomSymptoms
		data["HasArchivedSymptoms"] = viewData.Symptoms.HasArchivedSymptoms
		data["SymptomStatusMessage"] = ""
		data["SymptomErrorMessage"] = ""
		data["SymptomDraftName"] = ""
		data["SymptomDraftIcon"] = defaultSymptomDraftIcon("")
		data["SymptomIconOptions"] = buildSettingsSymptomIconOptions("")
	}

	return data, nil
}
