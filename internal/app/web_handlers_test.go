package app

import (
	"reflect"
	"testing"
	"time"
)

func TestFilterRecordEntriesDefaultKeepsLatestSevenAvailableDates(t *testing.T) {
	entries := []recordEntry{
		testRecordEntry(t, "2026-05-03"),
		testRecordEntry(t, "2026-04-29"),
		testRecordEntry(t, "2026-05-12"),
		testRecordEntry(t, "2026-05-07"),
		testRecordEntry(t, "2026-05-01"),
		testRecordEntry(t, "2026-05-11"),
		testRecordEntry(t, "2026-05-09"),
		testRecordEntry(t, "2026-05-05"),
	}

	got := recordFilePaths(filterRecordEntries(entries, recordDateRange{}))
	want := []string{
		"cam1/2026-05-12/2026-05-12.ts",
		"cam1/2026-05-11/2026-05-11.ts",
		"cam1/2026-05-09/2026-05-09.ts",
		"cam1/2026-05-07/2026-05-07.ts",
		"cam1/2026-05-05/2026-05-05.ts",
		"cam1/2026-05-03/2026-05-03.ts",
		"cam1/2026-05-01/2026-05-01.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFilterRecordEntriesExplicitRangeDoesNotBackfill(t *testing.T) {
	entries := []recordEntry{
		testRecordEntry(t, "2026-05-12"),
		testRecordEntry(t, "2026-05-10"),
		testRecordEntry(t, "2026-05-04"),
	}
	dateRange, err := parseRecordDateRange("2026-05-09", "2026-05-12")
	if err != nil {
		t.Fatal(err)
	}

	got := recordFilePaths(filterRecordEntries(entries, dateRange))
	want := []string{
		"cam1/2026-05-12/2026-05-12.ts",
		"cam1/2026-05-10/2026-05-10.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestParseRecordDateRangeLimitsSevenConsecutiveDays(t *testing.T) {
	if _, err := parseRecordDateRange("2026-05-01", "2026-05-07"); err != nil {
		t.Fatalf("expected 7-day range to pass, got %v", err)
	}
	if _, err := parseRecordDateRange("2026-05-01", "2026-05-08"); err == nil {
		t.Fatal("expected range longer than 7 days to fail")
	}
	if _, err := parseRecordDateRange("2026-05-02", ""); err == nil {
		t.Fatal("expected partial range to fail")
	}
	if _, err := parseRecordDateRange("2026-05-02", "2026-05-01"); err == nil {
		t.Fatal("expected inverted range to fail")
	}
}

func TestParseRecordDateFromPath(t *testing.T) {
	got, ok := parseRecordDateFromPath("cam1/2026-05-12/cam1_2026-05-12_10-20-30.mp4")
	if !ok {
		t.Fatal("expected date to be parsed from path")
	}
	want := time.Date(2026, 5, 12, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("expected %s, got %s", want, got)
	}
	if _, ok := parseRecordDateFromPath("cam1/2026-99-99/bad.mp4"); ok {
		t.Fatal("expected invalid date to be ignored")
	}
}

func testRecordEntry(t *testing.T, dateKey string) recordEntry {
	t.Helper()

	date, err := parseRecordDate(dateKey)
	if err != nil {
		t.Fatal(err)
	}
	return recordEntry{
		date:    date,
		dateKey: dateKey,
		file: recordFile{
			Name: dateKey + ".ts",
			Path: "cam1/" + dateKey + "/" + dateKey + ".ts",
		},
	}
}

func recordFilePaths(files []recordFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
