package usecase_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type fakeMethods map[string]bool // path -> server streaming.

func (f fakeMethods) MethodShape(_, path string) (serverStreaming, known bool) {
	v, ok := f[path]

	return v, ok
}

func TestMockService_StreamScriptNeedsStreamingGRPCMethod(t *testing.T) {
	t.Parallel()

	svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(),
		&mockContractEngine{}, &mockLLMGateway{}).
		WithGRPCMethods(fakeMethods{"/shop.v1.Shop/GetItem": false, "/shop.v1.Shop/ListItems": true})

	script := &domain.StreamScript{Steps: []domain.StreamStep{{Data: `{"id":"1"}`}}}
	rule := func(path string) *domain.MockRule {
		return &domain.MockRule{
			ProjectID: "p", Path: path, Method: http.MethodPost, StatusCode: http.StatusOK,
			ContentType: "application/json", Body: `{}`, Stream: script,
		}
	}

	_, err := svc.Create(rule("/shop.v1.Shop/GetItem"))
	if !errors.Is(err, usecase.ErrInvalidStream) {
		t.Errorf("unary method: want ErrInvalidStream, got %v", err)
	}

	if _, err = svc.Create(rule("/shop.v1.Shop/ListItems")); err != nil {
		t.Errorf("server-streaming method: %v", err)
	}

	// Paths the contract does not know are not gRPC methods: accepted as before.
	if _, err = svc.Create(rule("/events")); err != nil {
		t.Errorf("unknown path: %v", err)
	}

	// A plain unary rule without a script is fine.
	plain := rule("/shop.v1.Shop/GetItem")
	plain.Stream = nil

	if _, err = svc.Create(plain); err != nil {
		t.Errorf("unary rule without script: %v", err)
	}

	// Updating an existing rule is checked as well.
	created, err := svc.Create(plain)
	if err != nil {
		t.Fatal(err)
	}

	created.Stream = script

	if err = svc.Update(created); !errors.Is(err, usecase.ErrInvalidStream) {
		t.Errorf("update: want ErrInvalidStream, got %v", err)
	}
}
