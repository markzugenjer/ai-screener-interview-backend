package domain

import "testing"

func TestJobTextFallsBackPerField(t *testing.T) {
	j := &Job{Title: "Staff Klinik", Division: "Operasional", Category: "Non-Technical", Location: "Jakarta",
		EmploymentType: "Full-time", Description: "Deskripsi", Requirements: []string{"SMA"}, SourceLang: "id",
		Translations: map[string]JobText{"en": {Title: "Clinic Staff", Description: "Description"}}}

	if got := j.Text("id"); got.Title != "Staff Klinik" || got.Requirements[0] != "SMA" {
		t.Fatalf("source language: %+v", got)
	}
	en := j.Text("en")
	if en.Title != "Clinic Staff" || en.Description != "Description" {
		t.Fatalf("translated fields: %+v", en)
	}
	if en.Division != "Operasional" || en.Location != "Jakarta" || en.Requirements[0] != "SMA" {
		t.Fatalf("missing fields must fall back to the source: %+v", en)
	}
	if got := j.Text("fr"); got.Title != "Staff Klinik" {
		t.Fatalf("unknown language must fall back: %+v", got)
	}
	if (JobText{}).HasText() || !(JobText{Title: "x"}).HasText() {
		t.Fatal("HasText")
	}
}
