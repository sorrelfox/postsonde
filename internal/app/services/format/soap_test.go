package format

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/soap"
)

func TestSOAPEnvelope(t *testing.T) {
	t.Parallel()
	svc := New(nil)

	got, err := svc.SOAPEnvelope(t.Context(), SOAPInputDTO{Version: "1.1", Action: "urn:x:Ping", Text: "<Ping/>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, `xmlns:soap="`+soap.NS11+`"`) || !strings.Contains(got.Text, "<Ping/>") {
		t.Errorf("text = %q", got.Text)
	}
	want := []SOAPHeaderDTO{{"Content-Type", "text/xml; charset=utf-8"}, {"SOAPAction", `"urn:x:Ping"`}}
	if !reflect.DeepEqual(got.Headers, want) || got.Remove == nil || len(got.Remove) != 0 {
		t.Errorf("headers = %+v, remove = %#v", got.Headers, got.Remove)
	}

	// 1.2 switches the envelope, moves the action to Content-Type and
	// takes SOAPAction out.
	v12, err := svc.SOAPEnvelope(t.Context(), SOAPInputDTO{Version: "1.2", Action: "urn:x:Ping", Text: got.Text})
	if err != nil {
		t.Fatal(err)
	}
	if v12.Text != strings.ReplaceAll(got.Text, soap.NS11, soap.NS12) {
		t.Errorf("1.2 text = %q", v12.Text)
	}
	want = []SOAPHeaderDTO{{"Content-Type", `application/soap+xml; charset=utf-8; action="urn:x:Ping"`}}
	if !reflect.DeepEqual(v12.Headers, want) || !reflect.DeepEqual(v12.Remove, []string{"SOAPAction"}) {
		t.Errorf("1.2 headers = %+v, remove = %q", v12.Headers, v12.Remove)
	}

	_, err = svc.SOAPEnvelope(t.Context(), SOAPInputDTO{Version: "2.0"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeSOAPVersion {
		t.Errorf("bad version err = %v", err)
	}
}
