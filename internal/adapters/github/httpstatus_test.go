package github

import "testing"

func TestSplitHTTPResponse_ParsesHTTP1_1StatusLine(t *testing.T) {
	raw := []byte("HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\n\r\n{\"message\":\"Not Found\"}")
	status, body, err := splitHTTPResponse(raw)
	if err != nil {
		t.Fatalf("splitHTTPResponse: %v", err)
	}
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
	if string(body) != `{"message":"Not Found"}` {
		t.Errorf("body = %q", body)
	}
}

func TestSplitHTTPResponse_ParsesHTTP2StatusLine(t *testing.T) {
	raw := []byte("HTTP/2 410 Gone\nContent-Type: application/json\n\n{}")
	status, _, err := splitHTTPResponse(raw)
	if err != nil {
		t.Fatalf("splitHTTPResponse: %v", err)
	}
	if status != 410 {
		t.Errorf("status = %d, want 410", status)
	}
}

func TestSplitHTTPResponse_ParsesHTTP2_0StatusLine(t *testing.T) {
	raw := []byte("HTTP/2.0 404 Not Found\nContent-Type: application/json\n\n{}")
	status, _, err := splitHTTPResponse(raw)
	if err != nil {
		t.Fatalf("splitHTTPResponse: %v", err)
	}
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestSplitHTTPResponse_NoStatusLine_ReturnsError(t *testing.T) {
	raw := []byte("no status line here at all")
	_, _, err := splitHTTPResponse(raw)
	if err == nil {
		t.Fatal("want error for missing status line")
	}
}

func TestSplitHTTPResponse_UnrecognizedStatusLine_ReturnsError(t *testing.T) {
	raw := []byte("garbage 404 not a status line\n\nbody")
	_, _, err := splitHTTPResponse(raw)
	if err == nil {
		t.Fatal("want error for unrecognized status line")
	}
}
