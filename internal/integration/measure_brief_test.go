package integration_test

import (
	"bytes"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brief"
	"github.com/chka/os-brain/internal/doctor"
	"github.com/chka/os-brain/internal/measure"
)

// One infinite reading used to panic every summary of its series, brief and doctor
// included. Non-finite values and bands are refused, and one that is already stored
// crashes nothing; doctor names the command that removes it.
func TestANonFiniteMeasureIsRefusedAndAStoredOneCrashesNothing(t *testing.T) {
	s := newTestStore(t)
	if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "add", Key: "tmp", Value: 5}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "add", Key: "tmp", Value: v}); err == nil {
			t.Errorf("%v was stored", v)
		}
	}
	inf := math.Inf(1)
	if err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: "limit", Key: "tmp", High: &inf}); err == nil {
		t.Error("an infinite band was stored")
	}
	if _, err := s.DB.Exec("INSERT INTO measures(project,scope,key,label,value,unit,taken_on,"+
		"taken_at,source,confidence,ref,note,created_at) VALUES('testproj','global','tmp','tmp',?,"+
		"'','2026-01-01',1,'s','reported','','',1)", math.Inf(1)); err != nil {
		t.Fatal(err)
	}
	if err := brief.New(s).Brief(io.Discard, brief.BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	var doc bytes.Buffer
	if err := doctor.New(s).Doctor(&doc, doctor.DoctorArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.String(), "measure forget") {
		t.Errorf("doctor does not name the way out:\n%s", doc.String())
	}
}
