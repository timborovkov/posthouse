package pagination

import "testing"

func TestCursorRoundTripAndScopeBinding(t *testing.T) {
	type position struct {
		After string `json:"after"`
	}
	scope := struct {
		Query string `json:"query"`
	}{Query: "invoice"}
	token, err := Encode("messages", scope, position{After: "42"})
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	var got position
	if err := Decode(token, "messages", scope, &got); err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if got.After != "42" {
		t.Fatalf("Decode returned %#v", got)
	}
	if err := Decode(token, "messages", struct {
		Query string `json:"query"`
	}{Query: "other"}, &got); err == nil {
		t.Fatal("Decode accepted a cursor for another query")
	}
}

func TestDecodePositionStillRequiresSubsequentScopeValidation(t *testing.T) {
	type position struct {
		Range string `json:"range"`
	}
	token, err := Encode("events", struct {
		Query string `json:"query"`
	}{Query: "original"}, position{Range: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	var got position
	if err := DecodePosition(token, "events", &got); err != nil || got.Range != "saved" {
		t.Fatalf("DecodePosition returned %#v, %v", got, err)
	}
	if err := Decode(token, "events", struct {
		Query string `json:"query"`
	}{Query: "changed"}, &got); err == nil {
		t.Fatal("Decode accepted recovered position with changed scope")
	}
}

func TestPageSize(t *testing.T) {
	if got, err := PageSize(0, 25, 100); err != nil || got != 25 {
		t.Fatalf("PageSize returned %d, %v", got, err)
	}
	if _, err := PageSize(101, 25, 100); err == nil {
		t.Fatal("PageSize accepted a value above the maximum")
	}
}
