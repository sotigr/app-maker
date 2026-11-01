package app

import (
	"testing"

	manifestdomain "organon/internal/manifest/domain"
)

// Spec 11.5: lock files may only name allowed packages.
func TestCheckPython(t *testing.T) {
	lock := manifestdomain.File{Path: "nodes/requirements.lock", Data: []byte(`# generated
httpx==0.27.0 \
    --hash=sha256:abc
Pydantic_Core==2.1.0 --hash=sha256:def
numpy[extra]>=1.26
requests @ https://example.com/requests.whl
`)}
	probs := checkPython([]byte(`{"allowedPackages":["httpx","pydantic-core","numpy"]}`), []manifestdomain.File{lock})
	if len(probs) != 1 || probs[0].Pointer != "/line/6" {
		t.Fatalf("problems: %+v", probs)
	}
	if p := checkPython([]byte(`{}`), []manifestdomain.File{lock}); len(p) != 0 {
		t.Fatalf("no allowlist: %+v", p)
	}
}
