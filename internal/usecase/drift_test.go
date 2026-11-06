package usecase

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func jsonResp(status int, body string) (*MockResponse, *UpstreamResponse) {
	return &MockResponse{StatusCode: status, ContentType: "application/json; charset=utf-8", Body: []byte(body)},
		&UpstreamResponse{StatusCode: status, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(body)}
}

func TestCompareResponses_Structure(t *testing.T) {
	t.Parallel()

	mock, live := jsonResp(200, `{"a":1,"b":{"c":"x"},"items":[{"id":1}],"gone":true}`)
	live.Body = []byte(`{"a":"1","b":{"c":"y","d":1},"items":[{"id":2,"new":null}]}`)

	got := map[string]string{}
	for _, d := range compareResponses(mock, live) {
		got[d.Kind+":"+d.Path] = d.Mock + ">" + d.Live
	}

	want := map[string]string{
		"type-mismatch:a":            "number>string",
		"extra-in-live:b.d":          ">number",
		"extra-in-live:items[0].new": ">null",
		"missing-in-live:gone":       "boolean>",
	}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestCompareResponses_StatusShortCircuitsAndValuesAreIgnored(t *testing.T) {
	t.Parallel()

	mock, live := jsonResp(200, `{"a":1}`)
	live.StatusCode = 404

	diffs := compareResponses(mock, live)
	if len(diffs) != 1 || diffs[0].Kind != domain.DriftStatus {
		t.Errorf("a status difference is reported alone, got %+v", diffs)
	}

	mock, live = jsonResp(200, `{"a":1,"list":[1,2]}`)
	live.Body = []byte(`{"a":999,"list":[]}`)

	if diffs := compareResponses(mock, live); len(diffs) != 0 {
		t.Errorf("different values and empty arrays are not drift, got %+v", diffs)
	}
}

func TestRequestKey_IgnoresQueryOrderAndJSONLayout(t *testing.T) {
	t.Parallel()

	a := &TemplateContext{Method: "post", Path: "/u", Query: map[string][]string{"x": {"1"}, "y": {"2"}}, Body: []byte(`{"a":1,"b":[1,2]}`)}
	b := &TemplateContext{Method: "POST", Path: "/u", Query: map[string][]string{"y": {"2"}, "x": {"1"}}, Body: []byte("{\n\"b\":[1,2],\"a\":1}")}
	c := &TemplateContext{Method: "POST", Path: "/u", Query: map[string][]string{"y": {"2"}, "x": {"1"}}, Body: []byte(`{"a":1,"b":[2,1]}`)}

	if requestKey(a) != requestKey(b) {
		t.Error("equal requests must share a key")
	}

	if requestKey(a) == requestKey(c) {
		t.Error("different bodies must not share a key")
	}
}
