package logger

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	l := New()
	if l == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestInfo(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	l := New()
	l.Info("test message", "key1", "value1", "key2", 42)

	output := buf.String()
	if !strings.Contains(output, "INFO") {
		t.Error("expected INFO prefix")
	}
	if !strings.Contains(output, "test message") {
		t.Error("expected message")
	}
	if !strings.Contains(output, "key1=value1") {
		t.Error("expected key1=value1")
	}
	if !strings.Contains(output, "key2=42") {
		t.Error("expected key2=42")
	}
}

func TestError(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	l := New()
	l.Error("error message", "key", "value")

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Error("expected ERROR prefix")
	}
	if !strings.Contains(output, "error message") {
		t.Error("expected message")
	}
	if !strings.Contains(output, "key=value") {
		t.Error("expected key=value")
	}
}

func TestInfo_NoKeyVals(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	l := New()
	l.Info("just message")

	output := buf.String()
	if !strings.Contains(output, "INFO just message") {
		t.Errorf("expected 'INFO just message', got %q", output)
	}
}

func TestFormatKeyVals_OddCount(t *testing.T) {
	// Odd number of keyvals - last one should be printed without a value
	got := formatKeyVals("key1", "value1", "key2")
	if !strings.Contains(got, "key1=value1") {
		t.Error("expected key1=value1")
	}
	if !strings.Contains(got, "key2") {
		t.Error("expected key2")
	}
}

func TestFormatKeyVals_Empty(t *testing.T) {
	got := formatKeyVals()
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestFormatKeyVals_EvenCount(t *testing.T) {
	got := formatKeyVals("a", "1", "b", "2")
	if got != " a=1 b=2" {
		t.Errorf("expected ' a=1 b=2', got %q", got)
	}
}
