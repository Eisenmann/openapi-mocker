package memlog_test

import (
	"fmt"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/memlog"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestStore_AssignsIDsAndListsNewestFirst(t *testing.T) {
	t.Parallel()

	s := memlog.New(10)

	for i := range 3 {
		s.Add(&domain.RequestLog{ProjectID: "p", Path: fmt.Sprint("/", i)})
	}

	s.Add(&domain.RequestLog{ProjectID: "other", Path: "/o"})

	got := s.ListLogs("p", 2)
	if len(got) != 2 || got[0].Path != "/2" || got[1].Path != "/1" || got[0].ID == "" || got[0].ID == got[1].ID {
		t.Fatalf("got %+v", got)
	}
}

func TestStore_IsBounded(t *testing.T) {
	t.Parallel()

	s := memlog.New(5)

	for i := range 50 {
		s.Add(&domain.RequestLog{ProjectID: "p", Path: fmt.Sprint("/", i)})
	}

	got := s.ListLogs("p", 100)
	if len(got) != 5 || got[0].Path != "/49" || got[4].Path != "/45" {
		t.Fatalf("got %d entries, first %q last %q", len(got), got[0].Path, got[len(got)-1].Path)
	}
}

func TestStore_DefaultCapacity(t *testing.T) {
	t.Parallel()

	s := memlog.New(0)
	s.Add(&domain.RequestLog{ProjectID: "p"})

	if len(s.ListLogs("p", 10)) != 1 {
		t.Error("entry lost")
	}
}
