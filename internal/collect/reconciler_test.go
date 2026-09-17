package collect

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The reconciler workflow writes workflowRun.id and attempt as strings
// (GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT reach the step as strings); a JSON
// number decodes as well; anything else is refused naming the field.
func TestDecodeArtifactRunNumbers(t *testing.T) {
	const jsonNull = "null"
	cases := []struct {
		name, id, attempt string // raw JSON; "" leaves the key out
		wantID            int64
		wantAttempt       int
		wantErr           string
	}{
		{name: "numbers", id: `123`, attempt: `2`, wantID: 123, wantAttempt: 2},
		{name: "strings as the workflow writes them", id: `"123"`, attempt: `"2"`, wantID: 123, wantAttempt: 2},
		{name: "absent"},
		{name: "JSON null", id: jsonNull, attempt: jsonNull},
		{name: "non-numeric id", id: `"abc"`, attempt: `2`, wantErr: `workflowRun.id: "abc" is not a run number`},
		{name: "non-numeric attempt", id: `123`, attempt: `"two"`, wantErr: `workflowRun.attempt: "two" is not a run number`},
		{name: "fraction", id: `1.5`, attempt: `2`, wantErr: `workflowRun.id: 1.5 is not a run number`},
		{name: "bool", id: `true`, attempt: `2`, wantErr: `workflowRun.id: true is not a run number`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := []string{`"url": "https://github.com/giantswarm/github/actions/runs/123"`, `"event": "workflow_dispatch"`}
			if tc.id != "" {
				run = append(run, `"id": `+tc.id)
			}
			if tc.attempt != "" {
				run = append(run, `"attempt": `+tc.attempt)
			}
			body := `{"repository": "giantswarm/x", "finishedAt": "2026-09-17T17:00:00Z", "workflowRun": {` + strings.Join(run, ", ") + `}}`
			r, err := decodeArtifact(zipOf(t, ArtifactPrefix+"x.json", []byte(body)), 1<<20)
			if tc.wantErr != "" {
				if !errors.Is(err, errArtifactMalformed) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %v with %q, got %v", errArtifactMalformed, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.WorkflowRun.ID != tc.wantID || r.WorkflowRun.Attempt != tc.wantAttempt {
				t.Errorf("run %d attempt %d, want %d and %d", r.WorkflowRun.ID, r.WorkflowRun.Attempt, tc.wantID, tc.wantAttempt)
			}
			if r.WorkflowRun.URL == "" || r.WorkflowRun.Event != "workflow_dispatch" || r.Result.Repository != "giantswarm/x" || r.FinishedAt.IsZero() {
				t.Errorf("the rest of the report: %+v", r)
			}
		})
	}
}

// zipOf is a zip with one file, as the reconciler uploads it.
func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
