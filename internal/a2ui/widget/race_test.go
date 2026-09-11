package widget

import (
	"context"
	"fmt"
	"testing"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// TestSetValueConcurrentWithEnvelopeApply: widget data-model writes use
// Surface.SetDataModelPath, so they must not data-race with a concurrently
// applying engine (issue #36 part 4). Run under -race this pins the lock
// discipline; without the locked write path it reports races at the
// setValue / applyUpdateDataModel pair.
func TestSetValueConcurrentWithEnvelopeApply(t *testing.T) {
	eng := a2ui.NewEngine()
	errs := eng.Apply(context.Background(), []a2ui.Envelope{{
		Version: a2ui.VersionV1,
		CreateSurface: &a2ui.CreateSurface{
			SurfaceID:  "s",
			Components: []a2ui.Component{{ID: "root", Component: "row"}},
			DataModel:  map[string]any{"name": "a", "counter": 0},
		},
	}})
	if len(errs) > 0 {
		t.Fatalf("create surface: %v", errs)
	}
	surf := eng.Surface("s")
	m, err := New(surf)
	if err != nil {
		t.Fatalf("widget.New: %v", err)
	}
	f := &focusable{path: "/name"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			eng.Apply(context.Background(), []a2ui.Envelope{{
				UpdateDataModel: &a2ui.UpdateDataModel{
					SurfaceID: "s",
					Path:      "/counter",
					Value:     i,
				},
			}})
		}
	}()
	for i := 0; i < 300; i++ {
		if err := m.setValue(f, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("setValue: %v", err)
		}
		if v := boundValue(surf, "/name"); v == nil {
			t.Fatal("bound value vanished")
		}
		_, _ = surf.Materialize() // exercise locked reads
	}
	<-done
}
