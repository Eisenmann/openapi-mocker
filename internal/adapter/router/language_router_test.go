package router_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/router"
	"github.com/Eisenmann/openapi-mocker/internal/domain/ports"
)

func TestDefaultLanguageRouter_Go(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguageGo}
	got := r.SelectStrategy(ports.LanguageGo, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for Go, got %s", got)
	}
}

func TestDefaultLanguageRouter_TypeScript(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguageTypeScript}
	got := r.SelectStrategy(ports.LanguageTypeScript, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for TypeScript, got %s", got)
	}
}

func TestDefaultLanguageRouter_Python(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguagePython}
	got := r.SelectStrategy(ports.LanguagePython, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for Python, got %s", got)
	}
}

func TestDefaultLanguageRouter_CSharp(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguageCSharp}
	got := r.SelectStrategy(ports.LanguageCSharp, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for C#, got %s", got)
	}
}

func TestDefaultLanguageRouter_Java(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguageJava}
	got := r.SelectStrategy(ports.LanguageJava, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for Java, got %s", got)
	}
}

func TestDefaultLanguageRouter_Rust(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: ports.LanguageRust}
	got := r.SelectStrategy(ports.LanguageRust, req)

	if got != ports.StrategyNative {
		t.Errorf("expected StrategyNative for Rust, got %s", got)
	}
}

func TestDefaultLanguageRouter_UnknownLanguage(t *testing.T) {
	t.Parallel()

	r := router.NewDefaultLanguageRouter()

	req := &ports.GenerationRequest{Language: "unknown"}
	got := r.SelectStrategy("unknown", req)

	if got != ports.StrategyOpenAPI {
		t.Errorf("expected StrategyOpenAPI default for unknown language, got %s", got)
	}
}
