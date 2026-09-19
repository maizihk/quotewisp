package main

import "sentence-api/internal/observability"

type webPublicMetrics struct{ m *observability.Metrics }

func (a webPublicMetrics) Submission(result string) { a.m.WebSubmission(result) }

type webAdminMetrics struct{ m *observability.Metrics }

func (a webAdminMetrics) LoginAttempt(result string)   { a.m.WebAdminLogin(result) }
func (a webAdminMetrics) Review(action, result string) { a.m.WebReview(action, result) }
func (a webAdminMetrics) SentenceChange(action, result string) {
	a.m.WebSentenceChange(action, result)
}
func (a webAdminMetrics) CategoryChange(action, result string) {
	a.m.WebCategoryChange(action, result)
}

type webPublicDataMetrics struct{ m *observability.Metrics }

func (a webPublicDataMetrics) ExportBuilt(version uint64, bytes int) {
	a.m.WebPublicDataBuilt("success", version, bytes)
}
func (a webPublicDataMetrics) ExportFailed() { a.m.WebPublicDataBuilt("failure", 0, 0) }
