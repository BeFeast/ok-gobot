package videosummary

import "testing"
import "encoding/json"

func TestUploadResponseNumericJobID(t *testing.T) {
	body := []byte(`{"job_id":550,"url":"upload:x.mp4","status":"queued"}`)
	var created struct {
		ID    json.Number `json:"id"`
		JobID json.Number `json:"job_id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.JobID.String() != "550" {
		t.Fatalf("got %q", created.JobID.String())
	}
}
