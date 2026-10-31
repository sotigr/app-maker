package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	contentapi "app-maker/internal/content/api"
	contentapp "app-maker/internal/content/application"
	flowapp "app-maker/internal/flow/application"
	"app-maker/internal/kernel"
)

// beforeWriteAdapter connects Content's before-write hook to the flow
// engine; staged temporary references are renamed so that several flows
// and operations in one transaction never collide.
type beforeWriteAdapter struct {
	engine *flowapp.Engine
	n      atomic.Int64
}

func (a *beforeWriteAdapter) BeforeWrite(ctx context.Context, req contentapp.BeforeWriteRequest) (contentapp.BeforeWriteResult, error) {
	results, err := a.engine.BeforeWrite(ctx, string(req.Op), req.Type, req.Principal, req.Before, req.After)
	if err != nil {
		return contentapp.BeforeWriteResult{}, err
	}
	var out contentapp.BeforeWriteResult
	for _, r := range results {
		prefix := fmt.Sprintf("$bw%d_", a.n.Add(1))
		rename := func(s string) string {
			if strings.HasPrefix(s, "$") {
				return prefix + s[1:]
			}
			return s
		}
		for _, op := range r.Ops {
			out.Ops = append(out.Ops, contentapp.Op{Op: contentapp.OpKind(op.Op), Type: op.Type, Ref: rename(op.Ref),
				ID: rename(op.ID), Parent: rename(op.Parent), Version: op.Version, Data: op.Data})
		}
	}
	return out, nil
}

// runRecorder stores flow runs as urn:sys:feature:flowRun features (spec
// 10.12) through the system writer.
type runRecorder struct{ w contentapi.SystemWriter }

func (r runRecorder) Record(ctx context.Context, rec flowapp.RunRecord) error {
	data := map[string]any{
		"flow": rec.Flow, "flowVersion": rec.Version, "trigger": string(rec.Trigger), "status": rec.Status,
		"startedAt": rec.Started.UTC().Format(time.RFC3339Nano), "endedAt": rec.Ended.UTC().Format(time.RFC3339Nano),
	}
	if !rec.Principal.IsZero() {
		data["principal"] = rec.Principal.String()
	}
	if rec.Error != nil {
		data["error"] = rec.Error
	}
	if len(rec.Causation) > 0 {
		data["causation"] = rec.Causation
	}
	if rec.Nodes != nil {
		data["nodes"] = rec.Nodes
	}
	if rec.Trace != nil {
		data["trace"] = rec.Trace
	}
	_, err := r.w.Create(ctx, "flowRun", nil, toJSONValue(data), nil)
	return err
}

// envSecrets resolves ${secret:<name>} from FW_SECRET_<NAME> until the
// secret store arrives (spec 14.7).
func envSecrets(ref string) (string, bool) {
	name, ok := strings.CutPrefix(ref, "${secret:")
	if !ok {
		return "", false
	}
	name = strings.TrimSuffix(name, "}")
	key := "FW_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
	v := os.Getenv(key)
	return v, v != ""
}

// templateSources maps manifest templates to names without extension
// ("templates/email/late-task.txt" -> "email/late-task").
func templateSources(files []struct{ Path, Data string }) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		name := strings.TrimPrefix(f.Path, "templates/")
		name = strings.TrimSuffix(name, path.Ext(name))
		out[name] = f.Data
	}
	return out
}

func toJSONValue(v any) map[string]any {
	m, _ := normalize(v).(map[string]any)
	return m
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, sub := range x {
			out[k] = normalize(sub)
		}
		return out
	case kernel.ID:
		return x.String()
	}
	return v
}
