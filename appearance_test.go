package pdfo

import (
	"bytes"
	"testing"
)

func TestWinAnsi(t *testing.T) {
	if got := winAnsi("Înțelegere ăștia €5 “ok” 日"); !bytes.Equal(got, []byte("\xCEntelegere astia \x805 \x93ok\x94 ?")) {
		t.Errorf("got %q", got)
	}
	if w := helveticaWidth("Hi"); w != 722+222 {
		t.Errorf("width %v", w)
	}
}

func TestParseDA(t *testing.T) {
	size, color := parseDA("0 0 1 rg /Helv 10 Tf")
	if size != 10 || color != "0 0 1 rg" {
		t.Errorf("%v %q", size, color)
	}
	size, color = parseDA("/He 0 Tf")
	if size != 0 || color != "0 g" {
		t.Errorf("%v %q", size, color)
	}
}
